package artifact

// manifest.go — the public codec facade. Ties together the header
// (header.go), the deterministic JSON body (body.go), and the Sealed/
// Paranoid AEAD modes (crypto.go) into the operations the rest of the
// system uses: Encode / Decode / DecodeEncrypted / ComputeHandle /
// ComputeManifestDigest / VerifyManifestDigest.
//
// Two distinct identifiers (ADR-73):
//   - ArtifactID (handle) = PRF(NK, cd‖md): the floating external identity,
//     SERIALISED in the body, stable across form changes. Set by
//     ComputeHandle before encoding.
//   - ManifestDigest = hash of the entire file bytes (header included):
//     the on-disk filename and form-verifier, NOT serialised (it is the
//     hash of the body), recomputed by ComputeManifestDigest. Changes on
//     repack.

import (
	"encoding/hex"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/errs"
)

// identityMetaCanonEmpty is the canonical encoding of an empty identity
// partition — the v1 default (no fields are opted into identity). md =
// H(identityMetaCanonEmpty) is then a fixed token. The real canonical
// codec and the opt-in identity-field mechanism are deferred to the
// format-ADR; treat this marker as versioned and immutable.
var identityMetaCanonEmpty = []byte("scrinium/identity-meta/v1:{}")

// Encode produces the full file bytes (header + body) for a Plain
// manifest. Non-Plain crypto is rejected — use ComputeManifestDigest
// (which dispatches to the encrypted path) or the encrypted encode
// entrypoint.
//
// The handle (m.ArtifactID) IS part of the body and must already be set
// (ComputeHandle) before encoding. The ManifestDigest is NOT an input: it
// is the hash of these bytes.
func Encode(m domain.Manifest, encoding domain.ManifestEncoding, crypto domain.ManifestCrypto) ([]byte, error) {
	if crypto != "" && crypto != domain.ManifestCryptoPlain {
		return nil, errs.ErrUnsupportedCrypto
	}
	if err := checkRefLimits(m); err != nil {
		return nil, err
	}
	if err := validateSlot(m); err != nil {
		return nil, err
	}

	header, err := writeHeader(fileHeader{Encoding: encoding, Crypto: crypto})
	if err != nil {
		return nil, err
	}
	body, err := marshalBodyJSON(m)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(header)+len(body))
	out = append(out, header...)
	out = append(out, body...)
	return out, nil
}

// checkRefLimits enforces the per-field reference caps (ADR-93): blob_refs
// and handle_refs each fit a 16-bit count, so the chunk/member list is
// bounded at 65535. The encode path has no overall byte cap — it is bounded
// field-by-field; reads are guarded by MaxManifestSize (32 MiB, checked in
// Decode/DecodeEncrypted).
func checkRefLimits(m domain.Manifest) error {
	if len(m.BlobRefs) > domain.MaxBlobRefs || len(m.HandleRefs) > domain.MaxHandleRefs {
		return errs.ErrTooManyRefs
	}
	return nil
}

// validateSlot enforces the manifest slot invariant (ADR-92/104): a manifest
// is exactly one kind, decided by which identity slot is filled, and each kind
// carries the structure its kind requires. It runs at the encode boundary
// (Encode and encodeEncrypted), beside checkRefLimits, so a structurally
// invalid manifest is never serialised. The kinds:
//
//   - user (handle filled): the handle is PRF(NK, cd‖md), so it cannot exist
//     without its identity-meta (md = IdentityMetaHash).
//   - system (name filled): an inline artifact whose InlineBlob carries the
//     systemstore envelope (ADR-104); no handle machinery.
//   - container (both slots empty): a blob-backed pack / headless anchor — it
//     owns blobs, not an inline body, and has no handle machinery.
func validateSlot(m domain.Manifest) error {
	hasHandle := m.ArtifactID != ""
	hasName := m.Name != ""
	hasIdentityMeta := m.IdentityMetaHash != "" || len(m.IdentityNonce) != 0

	switch {
	case hasHandle && hasName:
		return fmt.Errorf("%w: both handle (%s) and name (%q) are set",
			errs.ErrInvalidManifestSlot, m.ArtifactID, m.Name)

	case hasHandle: // user
		if !hasIdentityMeta {
			return fmt.Errorf("%w: user artifact carries no identity-meta",
				errs.ErrInvalidManifestSlot)
		}

	case hasName: // system
		if len(m.InlineBlob) == 0 {
			return fmt.Errorf("%w: system artifact %q has no inline payload",
				errs.ErrInvalidManifestSlot, m.Name)
		}
		if hasIdentityMeta {
			return fmt.Errorf("%w: system artifact %q carries identity-meta",
				errs.ErrInvalidManifestSlot, m.Name)
		}

	default: // container — both slots empty
		if len(m.BlobRefs) == 0 {
			return fmt.Errorf("%w: container has no blob_refs",
				errs.ErrInvalidManifestSlot)
		}
		if len(m.InlineBlob) != 0 {
			return fmt.Errorf("%w: container carries an inline blob",
				errs.ErrInvalidManifestSlot)
		}
		if hasIdentityMeta {
			return fmt.Errorf("%w: container carries identity-meta",
				errs.ErrInvalidManifestSlot)
		}
	}

	// Layout coherence (ADR-66/92): inline content is embedded in the body,
	// not a physical blob, so an inline manifest carries no blob_ref — its
	// stored-form integrity rides the manifest digest, its identity is
	// content_hash. (The blob-backed direction is not enforced here: the
	// codec stays agnostic about a manifest that also carries inline bytes.)
	if m.LayoutHeader.BlobStorage == domain.LayoutInline && len(m.BlobRefs) != 0 {
		return fmt.Errorf("%w: inline manifest carries blob_refs",
			errs.ErrInvalidManifestSlot)
	}

	return nil
}

// Decode parses full manifest bytes (Plain only), validates the header,
// and returns the manifest with body fields populated — including the
// handle (m.ArtifactID), which now lives in the body. The ManifestDigest
// is NOT set — the caller derives and verifies it from the file bytes /
// path. An encrypted file returns ErrUnsupportedCrypto here; use
// DecodeEncrypted.
func Decode(data []byte) (domain.Manifest, error) {
	if len(data) > domain.MaxManifestSize {
		return domain.Manifest{}, errs.ErrManifestTooLarge
	}
	header, bodyOffset, err := readHeader(data)
	if err != nil {
		return domain.Manifest{}, err
	}
	if header.Crypto != domain.ManifestCryptoPlain {
		return domain.Manifest{}, errs.ErrUnsupportedCrypto
	}
	return unmarshalBodyJSON(data[bodyOffset:])
}

// DecodeEncrypted parses any manifest, decrypting the body when the header
// announces encryption. Plain files are forwarded to the Plain body
// decoder (no key needed). Encrypted files resolve their KeyID through
// keys and try each candidate DEK until one decrypts.
//
// keys may be nil only for a Plain file; an encrypted file with keys==nil
// surfaces ErrKeyNotFound. Failure classes: structural header errors (as
// in Decode); no resolver / zero candidates → ErrKeyNotFound; no candidate
// decrypts → ErrDecryptionFailed; decrypted-but-invalid JSON → wrapped error.
func DecodeEncrypted(data []byte, keys domain.KeyProvider) (domain.Manifest, error) {
	if len(data) > domain.MaxManifestSize {
		return domain.Manifest{}, errs.ErrManifestTooLarge
	}
	header, bodyOffset, err := readHeader(data)
	if err != nil {
		return domain.Manifest{}, err
	}

	if header.Crypto == domain.ManifestCryptoPlain {
		return unmarshalBodyJSON(data[bodyOffset:])
	}

	if keys == nil {
		return domain.Manifest{}, fmt.Errorf("%w: encrypted manifest, keyID=%q, no resolver",
			errs.ErrKeyNotFound, header.KeyID)
	}

	candidates, err := keys.GetKeys(header.KeyID)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifact.DecodeEncrypted: GetKeys: %w", err)
	}
	if len(candidates) == 0 {
		return domain.Manifest{}, fmt.Errorf("%w: keyID=%q", errs.ErrKeyNotFound, header.KeyID)
	}
	// candidates are DEK copies (resolvers hand out defensive copies) —
	// secret material. Wipe them on the way out so a long-running process
	// does not accumulate DEK copies on the heap awaiting GC.
	defer func() {
		for _, k := range candidates {
			aead.Wipe(k)
		}
	}()

	headerBytes := data[:bodyOffset]
	body := data[bodyOffset:]
	return decodeEncryptedBody(header.Crypto, body, candidates, headerBytes)
}

// ComputeHandle computes the floating ArtifactID (handle) for a manifest
// and populates m.ArtifactID, m.IdentityMetaHash (md) and m.IdentityNonce.
//
// md = H(canon(identity-meta)); in v1 the identity partition is empty, so
// md is a fixed token. handle = H(nk ‖ cd ‖ md ‖ nonce) (hashing.Handle).
// nk is the naming key (hashing.NamingKeyPublic in Plain/Sealed); nonce is
// fresh 16 random bytes in IdentityMode=Unique, nil in Coalesced. The
// caller (store) generates the nonce.
//
// Call ComputeHandle BEFORE encoding: the handle is part of the body, and
// the ManifestDigest is then the hash of the body that already carries it.
func ComputeHandle(
	m domain.Manifest,
	hashAlgo string,
	registry domain.HashRegistry,
	nk []byte,
	nonce []byte,
) (domain.Manifest, error) {
	h, err := registry.NewHasher(hashAlgo)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifact: hasher: %w", err)
	}
	if _, err := h.Write(identityMetaCanonEmpty); err != nil {
		return domain.Manifest{}, err
	}
	md := registry.Format(hashAlgo, h.Sum(nil))

	handle, err := hashing.Handle(registry, hashAlgo, nk, m.ContentHash, md, nonce)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifact: handle: %w", err)
	}
	m.ArtifactID = handle
	m.IdentityMetaHash = md
	m.IdentityNonce = nonce
	return m, nil
}

// ComputeManifestDigest encodes a manifest, hashes the resulting file
// bytes, and returns the ManifestDigest, the final file bytes (ready for
// driver.Put), and the in-memory manifest with Digest populated.
//
// The manifest must already carry its handle (ComputeHandle) — the handle
// is part of the body. Encoding dispatches on crypto: Plain → Encode
// (dek/keyID ignored); Sealed/Paranoid → the encrypted encode entrypoint
// with dek+keyID (an empty dek under non-Plain crypto is rejected there).
//
// ManifestDigest = hash(file bytes). One pass yields both the bytes and
// the digest.
func ComputeManifestDigest(
	m domain.Manifest,
	hashAlgo string,
	registry domain.HashRegistry,
	encoding domain.ManifestEncoding,
	crypto domain.ManifestCrypto,
	dek []byte,
	keyID string,
) (domain.ManifestDigest, []byte, domain.Manifest, error) {
	m.HashAlgo = hashAlgo // ADR-93: algorithm recorded once; refs/digest are bare hex
	var bytesEncoded []byte
	var err error

	switch {
	case crypto == "" || crypto == domain.ManifestCryptoPlain:
		bytesEncoded, err = Encode(m, encoding, crypto)
	default:
		bytesEncoded, err = encodeEncrypted(m, encoding, crypto, dek, keyID)
	}
	if err != nil {
		return "", nil, domain.Manifest{}, err
	}

	h, err := registry.NewHasher(hashAlgo)
	if err != nil {
		return "", nil, domain.Manifest{}, fmt.Errorf("artifact: hasher: %w", err)
	}
	if _, err := h.Write(bytesEncoded); err != nil {
		return "", nil, domain.Manifest{}, err
	}
	digest := domain.ManifestDigest(hex.EncodeToString(h.Sum(nil)))
	m.Digest = digest
	return digest, bytesEncoded, m, nil
}

// VerifyManifestDigest re-hashes file bytes and checks the digest against
// the expected ManifestDigest. The algorithm is the store's immutable
// ContentHasher (ADR-93: the digest is bare hex), supplied by the caller.
// A mismatch is ErrCorruptedManifest.
func VerifyManifestDigest(digest domain.ManifestDigest, fileBytes []byte, hashAlgo string, registry domain.HashRegistry) error {
	h, err := registry.NewHasher(hashAlgo)
	if err != nil {
		return fmt.Errorf("artifact: hasher %q: %w", hashAlgo, err)
	}
	if _, err := h.Write(fileBytes); err != nil {
		return err
	}
	got := domain.ManifestDigest(hex.EncodeToString(h.Sum(nil)))
	if got != digest {
		return errs.ErrCorruptedManifest
	}
	return nil
}

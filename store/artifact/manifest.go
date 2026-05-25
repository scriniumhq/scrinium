package artifact

// manifest.go — the public codec facade. Ties together the header
// (header.go), the deterministic JSON body (body.go), and the Sealed/
// Paranoid AEAD modes (crypto.go) into the operations the rest of the
// system uses: Encode / Decode / DecodeEncrypted / ComputeArtifactID /
// VerifyArtifactID.
//
// ArtifactID is the hash of the *entire file bytes*, header included. A
// manifest's serialised form is therefore stable: the same manifest plus
// the same key produces the same bytes and the same ID every time.

import (
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/store/internal/aead"
)

// KeyProvider is the minimal slice of a key resolver that DecodeEncrypted
// needs. Defining it here keeps the dependency one-way (artifact ← store):
// store's KeyResolver satisfies it implicitly; tests substitute a
// hand-rolled one.
type KeyProvider interface {
	GetKeys(keyID string) ([][]byte, error)
}

// Encode produces the full file bytes (header + body) for a Plain
// manifest. Non-Plain crypto is rejected — use ComputeArtifactID (which
// dispatches to the encrypted path) or the encrypted encode entrypoint.
//
// The manifest's ArtifactID is NOT an input: it is the result of hashing
// these bytes. Encode is for tests, for re-encoding after ID assignment,
// and for round-trip verification.
func Encode(m domain.Manifest, encoding domain.ManifestEncoding, crypto domain.ManifestCrypto) ([]byte, error) {
	if crypto != "" && crypto != domain.ManifestCryptoPlain {
		return nil, errs.ErrUnsupportedCrypto
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
	if len(out) > domain.MaxManifestSize {
		return nil, errs.ErrManifestTooLarge
	}
	return out, nil
}

// Decode parses full manifest bytes (Plain only), validates the header,
// and returns the manifest with body fields populated. The ArtifactID is
// NOT set — the caller decides whether to re-derive and verify it. An
// encrypted file returns ErrUnsupportedCrypto here; use DecodeEncrypted.
func Decode(data []byte) (domain.Manifest, error) {
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
func DecodeEncrypted(data []byte, keys KeyProvider) (domain.Manifest, error) {
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

// ComputeArtifactID encodes a manifest, hashes the resulting file bytes,
// and returns the assigned ArtifactID, the final file bytes (already
// carrying the ID-bearing manifest, ready for driver.Put), and the
// in-memory manifest with ArtifactID populated.
//
// Encoding dispatches on crypto: Plain → Encode (dek/keyID ignored);
// Sealed/Paranoid → the encrypted encode entrypoint with dek+keyID (an
// empty dek under non-Plain crypto is rejected there).
//
// ArtifactID = hash(file bytes), and the bytes include the body (which
// never contains ArtifactID itself — that field is in-memory only). One
// pass yields both the bytes and the ID.
func ComputeArtifactID(
	m domain.Manifest,
	hashAlgo string,
	registry domain.HashRegistry,
	encoding domain.ManifestEncoding,
	crypto domain.ManifestCrypto,
	dek []byte,
	keyID string,
) (domain.ArtifactID, []byte, domain.Manifest, error) {
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
	id := domain.ArtifactID(registry.Format(hashAlgo, h.Sum(nil)))
	m.ArtifactID = id
	return id, bytesEncoded, m, nil
}

// VerifyArtifactID re-hashes file bytes and checks the digest against id.
// The algorithm is taken from id's prefix, so a manifest can travel
// between Stores with different default hashers without losing its
// identity. A mismatch is ErrCorruptedManifest.
func VerifyArtifactID(id domain.ArtifactID, fileBytes []byte, registry domain.HashRegistry) error {
	algo, _, err := registry.Parse(string(id))
	if err != nil {
		return fmt.Errorf("artifact: parse id: %w", err)
	}
	h, err := registry.NewHasher(algo)
	if err != nil {
		return fmt.Errorf("artifact: hasher %q: %w", algo, err)
	}
	if _, err := h.Write(fileBytes); err != nil {
		return err
	}
	got := domain.ArtifactID(registry.Format(algo, h.Sum(nil)))
	if got != id {
		return errs.ErrCorruptedManifest
	}
	return nil
}

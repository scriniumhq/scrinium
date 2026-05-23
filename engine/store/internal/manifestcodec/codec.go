// Package manifestcodec serialises and deserialises manifest files
// according to docs/2. Internals/07 §7.1 (file header) and §7.5
// (deterministic body encoding).
//
// File layout per §7.1:
//
//	[0..3]      magic: \x00SC1 (JSON), \x00SC2 (Binary, deferred)
//	[4]         crypto flag: 0x00 Plain, 0x01 Sealed, 0x02 Paranoid
//	if crypto != 0x00:
//	  [5]       KeyID length L (0..255)
//	  [6..6+L]  KeyID bytes (UTF-8; absent when L == 0)
//	[...]       body
//
// Currently:
//   - JSON Plain is fully supported.
//   - JSON Sealed and Paranoid: header is parsed and
//     written, body encryption arrives in M2.3.2/M2.3.3.
//   - Binary (\x00SC2, MsgPack) returns ErrUnsupportedEncoding.
//
// ArtifactID is the hash of the *entire file bytes*, header
// included. The package exposes Encode/Decode that work on the
// file bytes, plus ComputeArtifactID that closes the loop:
// manifest -> encoded bytes -> hash -> ArtifactID assignment ->
// re-encoded bytes (to pin the field). A manifest's serialised
// form is therefore stable: the same manifest produces the same
// bytes every time.
package manifestcodec

// codec.go — Plain-mode public API surface for the package.
// EncodeFile/DecodeFile cover Plain manifests; encrypted modes
// live in encrypted.go; ComputeArtifactID and VerifyArtifactID
// are dispatch points used by both encryption tracks. The
// header layout, body JSON encoding, and per-mode ciphers are
// in header.go, body_json.go, and encrypted.go respectively.

import (
	"fmt"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// KeyProvider is the minimum slice of store.KeyResolver that
// DecodeFileEncrypted needs. Defining it locally lets manifestcodec
// stay independent of core (the package DAG is core ←
// manifestcodec, not the other way).
//
// Production callers pass a *store.KeyResolver, which satisfies
// this interface implicitly. Tests can substitute a hand-rolled
// resolver.
type KeyProvider interface {
	GetKeys(keyID string) ([][]byte, error)
}

// EncodeFile produces the full file bytes (header + body) for a
// manifest in JSON Plain format.
//
// The ArtifactID field of the manifest is NOT used as input — it
// is the *result* of hashing these very bytes. To produce a
// signed manifest:
//
//	bytes, _ := EncodeFile(m, ManifestEncodingJSON, ManifestCryptoPlain)
//	m.ArtifactID = hash(bytes) -> "<algo>-<hex>"
//
// Callers that need the round-trip primitive should use
// ComputeArtifactID below. EncodeFile alone is for tests, for
// re-encoding after ID assignment, and for round-trip verification
// in Verify.
func EncodeFile(m domain.Manifest, encoding domain.ManifestEncoding, crypto domain.ManifestCrypto) ([]byte, error) {
	// Header-only: M2.3.1 produces the §7.1 header for any
	// crypto mode but still emits the body as plain JSON. Real
	// body encryption arrives in M2.3.3 through a separate
	// signature (EncodeFileEncrypted). Until then, Sealed
	// and Paranoid produce headers that announce encryption but
	// carry plaintext bodies — useful only for header-level tests.
	// Public callers that pass non-Plain crypto here get
	// ErrUnsupportedCrypto, preserving the M1.4 behaviour.
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

// DecodeFile parses full manifest bytes, validates the header, and
// returns the manifest with all body fields populated. The
// returned manifest's ArtifactID is NOT set by this function — the
// caller owns deciding whether to re-derive it (and verify) or to
// accept it from a trusted source.
//
// Encoding mismatch: a manifest with the binary magic returns
// errs.ErrUnsupportedEncoding; an unknown magic returns a parse
// error. Crypto flag != Plain returns errs.ErrUnsupportedCrypto.
func DecodeFile(data []byte) (domain.Manifest, error) {
	header, bodyOffset, err := readHeader(data)
	if err != nil {
		return domain.Manifest{}, err
	}

	// Plain-only entry point. Encrypted bodies must go through
	// DecodeFileEncrypted with a KeyProvider attached.
	if header.Crypto != domain.ManifestCryptoPlain {
		return domain.Manifest{}, errs.ErrUnsupportedCrypto
	}

	return unmarshalBodyJSON(data[bodyOffset:])
}

// ComputeArtifactID encodes a manifest, hashes the resulting bytes
// with the given hasher, and returns both the assigned ArtifactID
// and the final file bytes. The returned bytes already carry the
// manifest with the populated ArtifactID — callers pass them
// straight to driver.Put.
//
// Encoding dispatches on crypto:
//   - Plain: EncodeFile (dek, keyID ignored).
//   - Sealed / Paranoid: EncodeFileEncrypted with the
//     supplied dek and keyID. Empty dek with non-Plain crypto
//     is rejected.
//
// Why the loop: ArtifactID = hash(file bytes), and the bytes
// include the manifest body (which does NOT contain ArtifactID
// itself — that field is on the in-memory struct, never on disk).
// One pass produces both the bytes and the ID; the manifest
// returned via the third value carries the ID for the in-memory
// path (the caller hands it to StoreIndex.IndexManifest).
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
		bytesEncoded, err = EncodeFile(m, encoding, crypto)
	default:
		bytesEncoded, err = EncodeFileEncrypted(m, encoding, crypto, dek, keyID)
	}
	if err != nil {
		return "", nil, domain.Manifest{}, err
	}

	h, err := registry.NewHasher(hashAlgo)
	if err != nil {
		return "", nil, domain.Manifest{}, fmt.Errorf("manifestcodec: hasher: %w", err)
	}
	if _, err := h.Write(bytesEncoded); err != nil {
		return "", nil, domain.Manifest{}, err
	}
	id := domain.ArtifactID(registry.Format(hashAlgo, h.Sum(nil)))
	m.ArtifactID = id
	return id, bytesEncoded, m, nil
}

// VerifyArtifactID re-hashes the given file bytes and checks the
// digest against the supplied id. Used on the read path: after
// downloading a manifest file, Verify confirms it has not been
// tampered with by recomputing the hash and comparing.
//
// algoFromID extracts the algorithm name from the id's prefix and
// uses it to pick a hasher from the registry. This way a manifest
// can travel between Stores with different default hashers without
// losing its identity.
func VerifyArtifactID(id domain.ArtifactID, fileBytes []byte, registry domain.HashRegistry) error {
	algo, _, err := registry.Parse(string(id))
	if err != nil {
		return fmt.Errorf("manifestcodec: parse id: %w", err)
	}
	h, err := registry.NewHasher(algo)
	if err != nil {
		return fmt.Errorf("manifestcodec: hasher %q: %w", algo, err)
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

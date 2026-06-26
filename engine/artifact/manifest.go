package artifact

// manifest.go — the public codec facade. Ties together the header
// (header.go), the deterministic JSON body (body.go), and the Sealed/
// Paranoid AEAD modes (crypto.go) into the operations the rest of the
// system uses: Encode / Decode / DecodeEncrypted / ComputeManifestDigest /
// VerifyManifestDigest.
//
// Note: Handle computation is decoupled in handle.go.

import (
	"encoding/hex"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/errs"
)

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

// Decode parses full manifest bytes (Plain only), validates the header,
// and returns the manifest with body fields populated.
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
// announces encryption.
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

	// Wipe DEK copies on the way out.
	defer func() {
		for _, k := range candidates {
			aead.Wipe(k)
		}
	}()

	headerBytes := data[:bodyOffset]
	body := data[bodyOffset:]
	return decodeEncryptedBody(header.Crypto, body, candidates, headerBytes)
}

// ComputeManifestDigest encodes a manifest, hashes the resulting file
// bytes, and returns the ManifestDigest, the final file bytes, and the
// in-memory manifest with Digest populated.
func ComputeManifestDigest(
	m domain.Manifest,
	hashAlgo string,
	registry domain.HashRegistry,
	encoding domain.ManifestEncoding,
	crypto domain.ManifestCrypto,
	dek []byte,
	keyID string,
) (domain.ManifestDigest, []byte, domain.Manifest, error) {
	m.HashAlgo = hashAlgo
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
// the expected ManifestDigest.
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

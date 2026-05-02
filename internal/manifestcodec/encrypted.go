package manifestcodec

// encrypted.go — MetadataOnly and Envelope crypto modes per
// docs/2. Internals/07 §7.4. EncodeFileEncrypted /
// DecodeFileEncrypted are the public entry points; the
// encode/decode helpers per-mode and the multi-DEK fallback
// in tryDecrypt live below them.
//
// KeyProvider is the narrow interface DecodeFileEncrypted
// needs from a key-resolution layer; production code passes
// a *core.KeyResolver.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/manifestcrypto"
)

// KeyProvider is the minimum slice of core.KeyResolver that
// DecodeFileEncrypted needs. Defining it locally lets manifestcodec
// stay independent of core (the package DAG is core ←
// manifestcodec, not the other way).
//
// Production callers pass a *core.KeyResolver, which satisfies
// this interface implicitly. Tests can substitute a hand-rolled
// resolver.
type KeyProvider interface {
	GetKeys(keyID string) ([][]byte, error)
}

func EncodeFileEncrypted(
	m domain.Manifest,
	encoding domain.ManifestEncoding,
	crypto domain.ManifestCrypto,
	dek []byte,
	keyID string,
) ([]byte, error) {
	if crypto == "" || crypto == domain.ManifestCryptoPlain {
		return nil, fmt.Errorf("manifestcodec.EncodeFileEncrypted: crypto=%q; use EncodeFile for Plain",
			crypto)
	}
	if len(dek) == 0 {
		return nil, fmt.Errorf("manifestcodec.EncodeFileEncrypted: empty dek")
	}

	header, err := writeHeader(fileHeader{
		Encoding: encoding,
		Crypto:   crypto,
		KeyID:    keyID,
	})
	if err != nil {
		return nil, err
	}

	var body []byte

	switch crypto {
	case domain.ManifestCryptoMetadataOnly:
		body, err = encodeMetadataOnly(m, dek, header)
	case domain.ManifestCryptoEnvelope:
		body, err = encodeEnvelope(m, dek, header)
	default:
		// cryptoFlag in writeHeader already filtered unknowns;
		// reaching here would mean a producer mismatch between
		// the two switches.
		return nil, errs.ErrUnsupportedCrypto
	}
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

// DecodeFileEncrypted parses any manifest, decrypting the body
// when the header announces encryption. Plain manifests are
// forwarded to DecodeFile (no key needed); encrypted manifests
// resolve their KeyID through keys and try each candidate DEK in
// turn until one decrypts.
//
// keys may be nil only when the file is Plain. Reaching an
// encrypted file with keys == nil surfaces ErrKeyNotFound.
//
// The set of failure classes:
//   - structural header errors → as in DecodeFile (parse error,
//     ErrUnsupportedEncoding, ErrUnsupportedCrypto)
//   - keys == nil on encrypted file → ErrKeyNotFound
//   - GetKeys returns 0 candidates → ErrKeyNotFound
//   - none of the candidates decrypts → ErrDecryptionFailed
//   - body decrypts but JSON is invalid → wrapped error
func DecodeFileEncrypted(data []byte, keys KeyProvider) (domain.Manifest, error) {
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
		return domain.Manifest{}, fmt.Errorf("manifestcodec.DecodeFileEncrypted: GetKeys: %w", err)
	}
	if len(candidates) == 0 {
		return domain.Manifest{}, fmt.Errorf("%w: keyID=%q", errs.ErrKeyNotFound, header.KeyID)
	}

	headerBytes := data[:bodyOffset]
	body := data[bodyOffset:]

	switch header.Crypto {
	case domain.ManifestCryptoMetadataOnly:
		return decodeMetadataOnly(body, candidates, headerBytes)
	case domain.ManifestCryptoEnvelope:
		return decodeEnvelope(body, candidates, headerBytes)
	default:
		return domain.Manifest{}, errs.ErrUnsupportedCrypto
	}
}

// --- MetadataOnly: encrypt only the metadata block ---

// encodeMetadataOnly produces the body bytes for MetadataOnly:
// JSON of the manifest with the metadata field replaced by a
// base64-wrapped AEAD ciphertext.
func encodeMetadataOnly(m domain.Manifest, dek, aad []byte) ([]byte, error) {
	// Encrypt the raw metadata bytes. Empty metadata is allowed —
	// we still seal the empty plaintext so the on-disk shape is
	// uniform (every MetadataOnly manifest has a non-empty
	// metadata field that is base64 of a 28-byte minimum
	// ciphertext).
	plaintext := []byte(m.Metadata)
	ciphertext, err := manifestcrypto.Seal(plaintext, dek, aad)
	if err != nil {
		return nil, fmt.Errorf("manifestcodec: seal metadata: %w", err)
	}

	// Wrap in JSON string: "BASE64==..."
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	wrapped, err := json.Marshal(encoded)
	if err != nil {
		return nil, fmt.Errorf("manifestcodec: marshal encrypted metadata: %w", err)
	}

	encrypted := m
	encrypted.Metadata = json.RawMessage(wrapped)
	return marshalBodyJSON(encrypted)
}

// decodeMetadataOnly parses MetadataOnly body bytes, decrypts
// the metadata field, and returns a manifest with plaintext
// metadata.
func decodeMetadataOnly(body []byte, candidates [][]byte, aad []byte) (domain.Manifest, error) {
	m, err := unmarshalBodyJSON(body)
	if err != nil {
		return domain.Manifest{}, err
	}

	// Decode the JSON-string-wrapped base64 ciphertext.
	var encoded string
	if err := json.Unmarshal(m.Metadata, &encoded); err != nil {
		return domain.Manifest{}, fmt.Errorf("%w: metadata field is not a JSON string",
			errs.ErrCorruptedManifest)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("%w: metadata not base64: %v",
			errs.ErrCorruptedManifest, err)
	}

	plaintext, err := tryDecrypt(ciphertext, candidates, aad)
	if err != nil {
		return domain.Manifest{}, err
	}

	if len(plaintext) == 0 {
		m.Metadata = nil
	} else {
		m.Metadata = json.RawMessage(plaintext)
	}
	return m, nil
}

// --- Envelope: encrypt the entire body ---

func encodeEnvelope(m domain.Manifest, dek, aad []byte) ([]byte, error) {
	plain, err := marshalBodyJSON(m)
	if err != nil {
		return nil, err
	}
	ciphertext, err := manifestcrypto.Seal(plain, dek, aad)
	if err != nil {
		return nil, fmt.Errorf("manifestcodec: seal envelope: %w", err)
	}
	return ciphertext, nil
}

func decodeEnvelope(body []byte, candidates [][]byte, aad []byte) (domain.Manifest, error) {
	plaintext, err := tryDecrypt(body, candidates, aad)
	if err != nil {
		return domain.Manifest{}, err
	}
	return unmarshalBodyJSON(plaintext)
}

// tryDecrypt walks the candidate keys and returns the first
// successful Open. All candidates failing surfaces
// ErrDecryptionFailed.
func tryDecrypt(ciphertext []byte, candidates [][]byte, aad []byte) ([]byte, error) {
	var lastErr error
	for _, dek := range candidates {
		plaintext, err := manifestcrypto.Open(ciphertext, dek, aad)
		if err == nil {
			return plaintext, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, errs.ErrDecryptionFailed
	}
	// No candidates iterated (slice was non-empty per caller) →
	// programmer error. Surface as decryption failed for caller
	// uniformity.
	return nil, errs.ErrDecryptionFailed
}

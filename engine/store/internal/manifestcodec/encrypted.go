package manifestcodec

// encrypted.go — Sealed and Paranoid crypto modes per
// docs/2. Internals/07 §7.1 + §7.4 + ADR-54.
//
// Sealed: the sys block stays in plain JSON; ext, usr, and
// inline_blob are encrypted as three independent AEAD blocks,
// each with its own IV and a shared KeyID. AAD for each block
// is the file header bytes concatenated with the block tag
// ("ext"/"usr"/"inline_blob") so a ciphertext cannot be
// re-purposed in a different field.
//
// Paranoid: the entire body (sys + ext + usr + inline_blob) is
// serialised as plain JSON and then encrypted as a single AEAD
// block with the file header as AAD.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
)

// AAD tags for the three Sealed sub-blocks. Concatenated with
// the file header bytes to produce a unique AAD per block, so
// ciphertexts cannot be cross-paired across fields.
var (
	aadTagExt    = []byte("ext")
	aadTagUsr    = []byte("usr")
	aadTagInline = []byte("inline_blob")
	aadTagSep    = []byte{0x00}
)

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
	case domain.ManifestCryptoSealed:
		body, err = encodeSealed(m, dek, header)
	case domain.ManifestCryptoParanoid:
		body, err = encodeParanoid(m, dek, header)
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
	// candidates is a slice of DEK copies (KeyResolver implementations
	// such as staticKeyResolver hand out defensive copies). They are
	// secret material; wipe them on the way out so a long-running
	// process does not accumulate copies of the active DEK in heap
	// for the GC to eventually collect.
	defer func() {
		for _, k := range candidates {
			aead.Wipe(k)
		}
	}()

	headerBytes := data[:bodyOffset]
	body := data[bodyOffset:]

	switch header.Crypto {
	case domain.ManifestCryptoSealed:
		return decodeSealed(body, candidates, headerBytes)
	case domain.ManifestCryptoParanoid:
		return decodeParanoid(body, candidates, headerBytes)
	default:
		return domain.Manifest{}, errs.ErrUnsupportedCrypto
	}
}

// --- Sealed: encrypt ext, usr, inline_blob as three blocks ---

// encodeSealed produces the body bytes for Sealed per ADR-54:
// sys stays in plain JSON; ext, usr, inline_blob are encrypted
// as independent AEAD blocks with per-block AAD tags. An empty
// block is omitted from the output rather than sealed empty —
// the on-disk shape matches Plain's "absent when empty" rule.
func encodeSealed(m domain.Manifest, dek, header []byte) ([]byte, error) {
	sealed := m

	if len(m.Ext) > 0 {
		ct, err := sealBlock(m.Ext, dek, header, aadTagExt)
		if err != nil {
			return nil, fmt.Errorf("manifestcodec: seal ext: %w", err)
		}
		sealed.Ext = wrapBase64AsJSONString(ct)
	} else {
		sealed.Ext = nil
	}

	if len(m.Usr) > 0 {
		ct, err := sealBlock(m.Usr, dek, header, aadTagUsr)
		if err != nil {
			return nil, fmt.Errorf("manifestcodec: seal usr: %w", err)
		}
		sealed.Usr = wrapBase64AsJSONString(ct)
	} else {
		sealed.Usr = nil
	}

	if len(m.InlineBlob) > 0 {
		ct, err := sealBlock(m.InlineBlob, dek, header, aadTagInline)
		if err != nil {
			return nil, fmt.Errorf("manifestcodec: seal inline_blob: %w", err)
		}
		// marshalBodyJSON will base64-encode this again. We
		// store the AEAD ciphertext as the raw bytes; the
		// resulting on-disk inline_blob is base64(ciphertext)
		// — single-encoded — exactly like Plain stores
		// base64(plaintext).
		sealed.InlineBlob = ct
	} else {
		sealed.InlineBlob = nil
	}

	return marshalBodyJSON(sealed)
}

// decodeSealed parses Sealed body bytes, decrypts each of the
// three optional sub-blocks (ext, usr, inline_blob), and
// returns a manifest with plaintext fields.
func decodeSealed(body []byte, candidates [][]byte, header []byte) (domain.Manifest, error) {
	m, err := unmarshalBodyJSON(body)
	if err != nil {
		return domain.Manifest{}, err
	}

	if len(m.Ext) > 0 {
		plain, err := openSealedField(m.Ext, candidates, header, aadTagExt)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: open ext: %w", err)
		}
		if len(plain) == 0 {
			m.Ext = nil
		} else {
			m.Ext = json.RawMessage(plain)
		}
	}

	if len(m.Usr) > 0 {
		plain, err := openSealedField(m.Usr, candidates, header, aadTagUsr)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: open usr: %w", err)
		}
		if len(plain) == 0 {
			m.Usr = nil
		} else {
			m.Usr = json.RawMessage(plain)
		}
	}

	if len(m.InlineBlob) > 0 {
		// In Sealed, m.InlineBlob holds the AEAD ciphertext —
		// unmarshalBodyJSON already base64-decoded the on-disk
		// representation into raw bytes. Decrypt in place.
		plain, err := tryDecrypt(m.InlineBlob, candidates, blockAAD(header, aadTagInline))
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("manifestcodec: open inline_blob: %w", err)
		}
		m.InlineBlob = plain
	}

	return m, nil
}

// sealBlock encrypts plaintext with the given DEK and a
// per-block AAD derived from the file header and a block tag.
func sealBlock(plaintext, dek, header, tag []byte) ([]byte, error) {
	return sealBody(plaintext, dek, blockAAD(header, tag))
}

// openSealedField decodes a JSON-string-wrapped base64
// ciphertext (as produced by wrapBase64AsJSONString), then
// runs the candidate DEKs against it with the per-block AAD.
func openSealedField(field json.RawMessage, candidates [][]byte, header, tag []byte) ([]byte, error) {
	var encoded string
	if err := json.Unmarshal(field, &encoded); err != nil {
		return nil, fmt.Errorf("%w: field is not a JSON string", errs.ErrCorruptedManifest)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: field not base64: %v", errs.ErrCorruptedManifest, err)
	}
	return tryDecrypt(ciphertext, candidates, blockAAD(header, tag))
}

// blockAAD builds the AAD for a Sealed sub-block:
// header bytes || 0x00 || tag. The 0x00 separator keeps the
// tag from being confusable with the trailing bytes of header
// (defence in depth — KeyID inside the header is UTF-8 and
// already non-NUL by construction, but the separator costs us
// one byte).
func blockAAD(header, tag []byte) []byte {
	out := make([]byte, 0, len(header)+1+len(tag))
	out = append(out, header...)
	out = append(out, aadTagSep...)
	out = append(out, tag...)
	return out
}

// wrapBase64AsJSONString turns raw ciphertext into a JSON-string
// of its base64 representation, ready to be embedded as the
// value of an ext/usr field in jsonBody.
func wrapBase64AsJSONString(raw []byte) json.RawMessage {
	encoded := base64.StdEncoding.EncodeToString(raw)
	// json.Marshal of a string is always safe — the only way
	// it can fail is OOM. Ignore the error for clarity.
	wrapped, _ := json.Marshal(encoded)
	return json.RawMessage(wrapped)
}

// --- Paranoid: encrypt the entire body ---

func encodeParanoid(m domain.Manifest, dek, aad []byte) ([]byte, error) {
	plain, err := marshalBodyJSON(m)
	if err != nil {
		return nil, err
	}
	ciphertext, err := sealBody(plain, dek, aad)
	if err != nil {
		return nil, fmt.Errorf("manifestcodec: seal Paranoid: %w", err)
	}
	return ciphertext, nil
}

func decodeParanoid(body []byte, candidates [][]byte, aad []byte) (domain.Manifest, error) {
	plaintext, err := tryDecrypt(body, candidates, aad)
	if err != nil {
		return domain.Manifest{}, err
	}
	return unmarshalBodyJSON(plaintext)
}

// tryDecrypt walks the candidate keys and returns the first
// successful Open. All candidates failing — including the
// degenerate empty-slice case — surfaces ErrDecryptionFailed.
func tryDecrypt(ciphertext []byte, candidates [][]byte, aad []byte) ([]byte, error) {
	for _, dek := range candidates {
		plaintext, err := openBody(ciphertext, dek, aad)
		if err == nil {
			return plaintext, nil
		}
	}
	return nil, errs.ErrDecryptionFailed
}

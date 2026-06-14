package artifact

// crypto.go — Sealed and Paranoid manifest crypto per docs/2 Internals/07
// §7.1 + §7.4 + ADR-54, plus the body-AEAD primitive they share.
//
// Sealed: the sys block stays plain JSON; ext, usr, and inline_blob are
// each encrypted as an independent AEAD block (own IV, shared KeyID). Each
// block's AAD is the file header bytes ‖ 0x00 ‖ block tag, so a ciphertext
// cannot be re-purposed in a different field.
//
// Paranoid: the entire body (sys+ext+usr+inline_blob) is serialised as
// plain JSON and encrypted as a single AEAD block with the file header as
// AAD.
//
// The body AEAD here is single-buffer, nonce-prefixed AES-GCM (layout:
// nonce ‖ ciphertext ‖ tag). It is distinct from the streaming/segmented
// blob AEAD (which lives with the pipeline); both share only the cipher
// construction in internal/aead.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/errs"
)

// minCiphertext is the smallest valid body ciphertext: a nonce prefix and
// a tag, with zero plaintext between.
const minCiphertext = aead.NonceLen + aead.TagLen

// AAD tags for the three Sealed sub-blocks, concatenated with the header
// (via blockAAD) so ciphertexts cannot be cross-paired across fields.
var (
	aadTagExt    = []byte("ext")
	aadTagUsr    = []byte("usr")
	aadTagInline = []byte("inline_blob")
	aadTagSep    = []byte{0x00}
)

// --- body AEAD primitive ---

// sealBody encrypts plaintext with dek under AES-256-GCM, binding aadBytes
// via the auth tag. Output: nonce(12, random) ‖ ciphertext ‖ tag(16). The
// nonce is fresh per call — a sealBody output must never be re-sealed
// (nonce reuse is fatal to AES-GCM).
func sealBody(plaintext, dek, aadBytes []byte) ([]byte, error) {
	gcm, err := aead.NewGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("artifact.sealBody: %w", err)
	}
	nonce := make([]byte, aead.NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("artifact.sealBody: nonce: %w", err)
	}
	// Seal appends ciphertext+tag to dst; passing nonce as dst lays out
	// nonce ‖ ciphertext ‖ tag exactly as the wire format expects.
	return gcm.Seal(nonce, nonce, plaintext, aadBytes), nil
}

// openBody decrypts a sealBody output. Tag mismatch (wrong DEK, modified
// ciphertext, or modified AAD) is collapsed into ErrDecryptionFailed as a
// defence against side-channel oracles.
func openBody(ciphertext, dek, aadBytes []byte) ([]byte, error) {
	if len(ciphertext) < minCiphertext {
		return nil, fmt.Errorf("artifact.openBody: ciphertext too short (%d bytes, need at least %d)",
			len(ciphertext), minCiphertext)
	}
	gcm, err := aead.NewGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("artifact.openBody: %w", err)
	}
	nonce := ciphertext[:aead.NonceLen]
	body := ciphertext[aead.NonceLen:]
	plaintext, err := gcm.Open(nil, nonce, body, aadBytes)
	if err != nil {
		return nil, errs.ErrDecryptionFailed
	}
	return plaintext, nil
}

// --- encrypted encode entrypoint (Sealed / Paranoid) ---

// encodeEncrypted produces the full file bytes (header ‖ encrypted body)
// for a non-Plain crypto mode. Plain is rejected — the caller routes Plain
// through the unencrypted Encode path.
func encodeEncrypted(
	m domain.Manifest,
	encoding domain.ManifestEncoding,
	crypto domain.ManifestCrypto,
	dek []byte,
	keyID string,
) ([]byte, error) {
	if crypto == "" || crypto == domain.ManifestCryptoPlain {
		return nil, fmt.Errorf("artifact.encodeEncrypted: crypto=%q; use Encode for Plain", crypto)
	}
	if len(dek) == 0 {
		return nil, fmt.Errorf("artifact.encodeEncrypted: empty dek")
	}
	if err := checkRefLimits(m); err != nil {
		return nil, err
	}

	header, err := writeHeader(fileHeader{Encoding: encoding, Crypto: crypto, KeyID: keyID})
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
		return nil, errs.ErrUnsupportedCrypto
	}
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(header)+len(body))
	out = append(out, header...)
	out = append(out, body...)
	return out, nil
}

// decodeEncryptedBody dispatches an already-parsed encrypted file to the
// Sealed or Paranoid decoder. headerBytes is the raw header (AAD input);
// body is the bytes after the header. candidates are the resolved DEKs.
func decodeEncryptedBody(crypto domain.ManifestCrypto, body []byte, candidates [][]byte, headerBytes []byte) (domain.Manifest, error) {
	switch crypto {
	case domain.ManifestCryptoSealed:
		return decodeSealed(body, candidates, headerBytes)
	case domain.ManifestCryptoParanoid:
		return decodeParanoid(body, candidates, headerBytes)
	default:
		return domain.Manifest{}, errs.ErrUnsupportedCrypto
	}
}

// --- Sealed ---

// encodeSealed seals ext, usr, inline_blob as independent AEAD blocks; sys
// stays plain. An empty block is omitted (matching Plain's absent-when-empty
// rule) rather than sealed empty.
func encodeSealed(m domain.Manifest, dek, header []byte) ([]byte, error) {
	sealed := m

	if len(m.Ext) > 0 {
		ct, err := sealBlock(m.Ext, dek, header, aadTagExt)
		if err != nil {
			return nil, fmt.Errorf("artifact: seal ext: %w", err)
		}
		sealed.Ext = wrapBase64AsJSONString(ct)
	} else {
		sealed.Ext = nil
	}

	if len(m.Usr) > 0 {
		ct, err := sealBlock(m.Usr, dek, header, aadTagUsr)
		if err != nil {
			return nil, fmt.Errorf("artifact: seal usr: %w", err)
		}
		sealed.Usr = wrapBase64AsJSONString(ct)
	} else {
		sealed.Usr = nil
	}

	if len(m.InlineBlob) > 0 {
		ct, err := sealBlock(m.InlineBlob, dek, header, aadTagInline)
		if err != nil {
			return nil, fmt.Errorf("artifact: seal inline_blob: %w", err)
		}
		// marshalBodyJSON base64-encodes this; the on-disk inline_blob is
		// base64(ciphertext) — single-encoded, like Plain's base64(plaintext).
		sealed.InlineBlob = ct
	} else {
		sealed.InlineBlob = nil
	}

	return marshalBodyJSON(sealed)
}

// decodeSealed parses the body and decrypts the three optional sub-blocks.
func decodeSealed(body []byte, candidates [][]byte, header []byte) (domain.Manifest, error) {
	m, err := unmarshalBodyJSON(body)
	if err != nil {
		return domain.Manifest{}, err
	}

	if len(m.Ext) > 0 {
		plain, err := openSealedField(m.Ext, candidates, header, aadTagExt)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: open ext: %w", err)
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
			return domain.Manifest{}, fmt.Errorf("artifact: open usr: %w", err)
		}
		if len(plain) == 0 {
			m.Usr = nil
		} else {
			m.Usr = json.RawMessage(plain)
		}
	}

	if len(m.InlineBlob) > 0 {
		// In Sealed, m.InlineBlob holds the AEAD ciphertext — unmarshalBodyJSON
		// already base64-decoded the on-disk form into raw bytes.
		plain, err := tryDecrypt(m.InlineBlob, candidates, blockAAD(header, aadTagInline))
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("artifact: open inline_blob: %w", err)
		}
		m.InlineBlob = plain
	}

	return m, nil
}

// sealBlock encrypts plaintext with a per-block AAD (header + tag).
func sealBlock(plaintext, dek, header, tag []byte) ([]byte, error) {
	return sealBody(plaintext, dek, blockAAD(header, tag))
}

// openSealedField decodes a JSON-string-wrapped base64 ciphertext (as
// produced by wrapBase64AsJSONString) and decrypts it with the per-block AAD.
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

// blockAAD builds a Sealed sub-block AAD: header ‖ 0x00 ‖ tag. The NUL
// separator keeps the tag from being confusable with header's trailing
// bytes (defence in depth).
func blockAAD(header, tag []byte) []byte {
	out := make([]byte, 0, len(header)+1+len(tag))
	out = append(out, header...)
	out = append(out, aadTagSep...)
	out = append(out, tag...)
	return out
}

// wrapBase64AsJSONString turns raw ciphertext into a JSON-string of its
// base64 form, ready to embed as an ext/usr field value.
func wrapBase64AsJSONString(raw []byte) json.RawMessage {
	encoded := base64.StdEncoding.EncodeToString(raw)
	wrapped, _ := json.Marshal(encoded) // marshalling a string only fails on OOM
	return json.RawMessage(wrapped)
}

// --- Paranoid ---

func encodeParanoid(m domain.Manifest, dek, aad []byte) ([]byte, error) {
	plain, err := marshalBodyJSON(m)
	if err != nil {
		return nil, err
	}
	ciphertext, err := sealBody(plain, dek, aad)
	if err != nil {
		return nil, fmt.Errorf("artifact: seal Paranoid: %w", err)
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

// tryDecrypt returns the first candidate DEK that Opens the ciphertext.
// All failing — including the empty-slice case — surfaces ErrDecryptionFailed.
func tryDecrypt(ciphertext []byte, candidates [][]byte, aad []byte) ([]byte, error) {
	for _, dek := range candidates {
		plaintext, err := openBody(ciphertext, dek, aad)
		if err == nil {
			return plaintext, nil
		}
	}
	return nil, errs.ErrDecryptionFailed
}

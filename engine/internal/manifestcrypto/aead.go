package manifestcrypto

import (
	"crypto/rand"
	"fmt"

	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
)

// minCiphertext is the smallest valid ciphertext: a nonce prefix and
// a tag, with zero plaintext bytes between. The wire layout below is
// nonce ‖ ciphertext ‖ tag; the AES-GCM construction and key length
// live in internal/aead (the shared primitive).
const minCiphertext = aead.NonceLen + aead.TagLen

// Seal encrypts plaintext with dek under AES-256-GCM, binding the
// supplied aad to the ciphertext via the auth tag.
//
// Output layout: nonce (12 bytes, random) | ciphertext | tag (16 bytes).
//
// The nonce is generated fresh from crypto/rand on every call;
// callers must NOT reuse a Seal output as input to a second call —
// that would amount to nonce reuse, the failure mode AES-GCM does
// not survive.
//
// Errors:
//   - len(dek) != aead.DEKLen → wrapped error (programmer mistake).
//   - crypto/rand failure → wrapped error (host RNG broken).
//   - cipher construction failure → wrapped error.
func Seal(plaintext, dek, aadBytes []byte) ([]byte, error) {
	gcm, err := aead.NewGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("manifestcrypto.Seal: %w", err)
	}

	nonce := make([]byte, aead.NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("manifestcrypto.Seal: nonce: %w", err)
	}

	// gcm.Seal appends ciphertext+tag to the dst buffer; passing
	// nonce as dst means the result is laid out exactly as the wire
	// format expects: nonce ‖ ciphertext ‖ tag.
	out := gcm.Seal(nonce, nonce, plaintext, aadBytes)
	return out, nil
}

// Open decrypts a ciphertext produced by Seal. The nonce is read
// from the leading bytes; the trailing 16 bytes are the auth tag
// that AES-GCM verifies before returning plaintext.
//
// Errors:
//   - len(dek) != aead.DEKLen → wrapped error.
//   - len(ciphertext) < minCiphertext → wrapped error (truncated
//     data — fundamentally unrecoverable).
//   - tag mismatch (wrong DEK, modified ciphertext, or modified aad)
//     → errs.ErrDecryptionFailed. The three failure modes are
//     deliberately collapsed: AEAD design refuses to distinguish them
//     as a defence against side-channel oracles.
func Open(ciphertext, dek, aadBytes []byte) ([]byte, error) {
	if len(ciphertext) < minCiphertext {
		return nil, fmt.Errorf("manifestcrypto.Open: ciphertext too short (%d bytes, need at least %d)",
			len(ciphertext), minCiphertext)
	}

	gcm, err := aead.NewGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("manifestcrypto.Open: %w", err)
	}

	nonce := ciphertext[:aead.NonceLen]
	body := ciphertext[aead.NonceLen:]

	plaintext, err := gcm.Open(nil, nonce, body, aadBytes)
	if err != nil {
		// Fold all AEAD verification failures into the canonical
		// "decryption failed" sentinel.
		return nil, errs.ErrDecryptionFailed
	}
	return plaintext, nil
}

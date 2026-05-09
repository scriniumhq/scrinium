package manifestcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"github.com/rkurbatov/scrinium/engine/errs"
)

// DEKLen is the required DEK byte length. AES-256-GCM key.
const DEKLen = 32

// nonceLen is the AES-GCM nonce size in bytes — fixed at 12 by
// the standard. Prepended to every ciphertext so Open can read
// it before deriving the AEAD instance.
const nonceLen = 12

// tagLen is the AES-GCM authentication tag size — fixed at 16.
// Not added to the wire layout explicitly; cipher.AEAD.Seal
// appends it to ciphertext, Open consumes it. Stored here only
// for the minimum-ciphertext-length check.
const tagLen = 16

// minCiphertext is the smallest valid ciphertext: a nonce
// prefix and a tag, with zero plaintext bytes between.
const minCiphertext = nonceLen + tagLen

// Seal encrypts plaintext with dek under AES-256-GCM, binding
// the supplied aad to the ciphertext via the auth tag.
//
// Output layout: nonce (12 bytes, random) | ciphertext | tag (16 bytes).
//
// The nonce is generated fresh from crypto/rand on every call;
// callers must NOT reuse a Seal output as input to a second
// call — that would amount to nonce reuse, the failure mode
// AES-GCM does not survive.
//
// Errors:
//   - len(dek) != DEKLen → wrapped error (programmer mistake).
//   - crypto/rand failure → wrapped error (host RNG broken).
//   - cipher.NewGCM failure → wrapped error (impossible on a
//     32-byte AES key, but checked for completeness).
func Seal(plaintext, dek, aad []byte) ([]byte, error) {
	if len(dek) != DEKLen {
		return nil, fmt.Errorf("manifestcrypto.Seal: dek length %d, want %d",
			len(dek), DEKLen)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("manifestcrypto.Seal: NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("manifestcrypto.Seal: NewGCM: %w", err)
	}

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("manifestcrypto.Seal: nonce: %w", err)
	}

	// Allocate output: nonce ‖ Seal(plaintext + tag).
	// gcm.Seal appends ciphertext+tag to the dst buffer; passing
	// nonce as dst means the result is laid out exactly as the
	// wire format expects.
	out := gcm.Seal(nonce, nonce, plaintext, aad)
	return out, nil
}

// Open decrypts a ciphertext produced by Seal. The nonce is read
// from the leading bytes; the trailing 16 bytes are the auth tag
// that AES-GCM verifies before returning plaintext.
//
// Errors:
//   - len(dek) != DEKLen → wrapped error.
//   - len(ciphertext) < minCiphertext → wrapped error
//     (truncated data — fundamentally unrecoverable).
//   - tag mismatch (wrong DEK, modified ciphertext, or modified
//     aad) → errs.ErrDecryptionFailed. The three failure modes
//     are deliberately collapsed: AEAD design refuses to
//     distinguish them as a defence against side-channel
//     oracles.
func Open(ciphertext, dek, aad []byte) ([]byte, error) {
	if len(dek) != DEKLen {
		return nil, fmt.Errorf("manifestcrypto.Open: dek length %d, want %d",
			len(dek), DEKLen)
	}
	if len(ciphertext) < minCiphertext {
		return nil, fmt.Errorf("manifestcrypto.Open: ciphertext too short (%d bytes, need at least %d)",
			len(ciphertext), minCiphertext)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("manifestcrypto.Open: NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("manifestcrypto.Open: NewGCM: %w", err)
	}

	nonce := ciphertext[:nonceLen]
	body := ciphertext[nonceLen:]

	plaintext, err := gcm.Open(nil, nonce, body, aad)
	if err != nil {
		// Fold all AEAD verification failures into the canonical
		// "decryption failed" sentinel. The underlying error is
		// generic ("cipher: message authentication failed"); we
		// do not wrap it because the caller cares about the
		// failure class, not the specific gcm internals.
		return nil, errs.ErrDecryptionFailed
	}
	return plaintext, nil
}

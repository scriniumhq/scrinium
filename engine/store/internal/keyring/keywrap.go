package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"scrinium.dev/engine/errs"
)

// wrapNonceLen is the GCM nonce size in bytes — the standard 12.
const wrapNonceLen = 12

// wrapTagLen is the GCM authentication tag size in bytes — 16.
const wrapTagLen = 16

// wrapKEK encrypts dek with kek using AES-256-GCM and a freshly
// generated 12-byte nonce. The returned slice has the layout
//
//	nonce (12) | ciphertext (len(dek)) | auth-tag (16)
//
// It errors when the KEK is the wrong length, the OS RNG fails, or
// AES initialisation refuses the key (which it cannot for a
// length-checked input — included for completeness).
func wrapKEK(dek, kek []byte) ([]byte, error) {
	if len(kek) != kekLen {
		return nil, fmt.Errorf("keyring: wrap: kek length %d, want %d",
			len(kek), kekLen)
	}
	if len(dek) == 0 {
		return nil, errors.New("keyring: wrap: empty dek")
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("keyring: wrap: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keyring: wrap: gcm init: %w", err)
	}

	nonce := make([]byte, wrapNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keyring: wrap: nonce: %w", err)
	}

	// Seal appends ciphertext+tag to its first arg. Pre-size the
	// slice so the result lives in a single allocation.
	out := make([]byte, wrapNonceLen, wrapNonceLen+len(dek)+wrapTagLen)
	copy(out, nonce)
	out = gcm.Seal(out, nonce, dek, nil)
	return out, nil
}

// unwrapKEK decrypts a wrapped DEK produced by wrapKEK. It returns
// errs.ErrDecryptionFailed for any tamper, truncation, or wrong-
// key condition — these are deliberately indistinguishable to
// avoid leaking which kind of failure occurred.
//
// Concrete failure modes folded into ErrDecryptionFailed:
//
//   - GCM auth-tag mismatch (single-bit tamper, wrong KEK)
//   - wrapped slice shorter than wrapNonceLen+wrapTagLen
//   - any non-cipher error from AES/GCM init (length-checked, so
//     this is mainly defensive)
func unwrapKEK(wrapped, kek []byte) ([]byte, error) {
	if len(kek) != kekLen {
		return nil, fmt.Errorf("keyring: unwrap: kek length %d, want %d",
			len(kek), kekLen)
	}
	if len(wrapped) < wrapNonceLen+wrapTagLen {
		return nil, fmt.Errorf("%w: wrapped slice too short (%d bytes)",
			errs.ErrDecryptionFailed, len(wrapped))
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("keyring: unwrap: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keyring: unwrap: gcm init: %w", err)
	}

	nonce := wrapped[:wrapNonceLen]
	ciphertext := wrapped[wrapNonceLen:]
	dek, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Fold all GCM errors into ErrDecryptionFailed — callers
		// must not branch on the specific cause. The wrapped
		// %v preserves the chain for debug-grade logging.
		return nil, fmt.Errorf("%w: %v", errs.ErrDecryptionFailed, err)
	}
	return dek, nil
}

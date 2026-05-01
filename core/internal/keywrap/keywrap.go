package keywrap

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/rkurbatov/scrinium/errs"
)

// KEKLen is the only supported KEK size. AES-256-GCM means a
// 32-byte key. kdf.KEKLen is fixed at the same value; passing any
// other length is a programming error.
const KEKLen = 32

// NonceLen is the GCM nonce size in bytes — the standard 12.
const NonceLen = 12

// TagLen is the GCM authentication tag size in bytes — 16.
const TagLen = 16

// Wrap encrypts dek with kek using AES-256-GCM and a freshly
// generated 12-byte nonce. The returned slice has the layout
//
//	nonce (12) | ciphertext (len(dek)) | auth-tag (16)
//
// Wrap returns an error when the KEK is the wrong length, the OS
// RNG fails, or AES initialisation refuses the key (which it
// cannot for a length-checked input — included for completeness).
func Wrap(dek, kek []byte) ([]byte, error) {
	if len(kek) != KEKLen {
		return nil, fmt.Errorf("keywrap.Wrap: kek length %d, want %d",
			len(kek), KEKLen)
	}
	if len(dek) == 0 {
		return nil, errors.New("keywrap.Wrap: empty dek")
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("keywrap.Wrap: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keywrap.Wrap: gcm init: %w", err)
	}

	nonce := make([]byte, NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keywrap.Wrap: nonce: %w", err)
	}

	// Seal appends ciphertext+tag to its first arg. Pre-size the
	// slice so the result lives in a single allocation.
	out := make([]byte, NonceLen, NonceLen+len(dek)+TagLen)
	copy(out, nonce)
	out = gcm.Seal(out, nonce, dek, nil)
	return out, nil
}

// Unwrap decrypts a wrapped DEK produced by Wrap. It returns
// errs.ErrDecryptionFailed for any tamper, truncation, or wrong-
// key condition — these are deliberately indistinguishable to
// avoid leaking which kind of failure occurred.
//
// Concrete failure modes folded into ErrDecryptionFailed:
//
//   - GCM auth-tag mismatch (single-bit tamper, wrong KEK)
//   - wrapped slice shorter than NonceLen+TagLen
//   - any non-cipher error from AES/GCM init (length-checked, so
//     this is mainly defensive)
func Unwrap(wrapped, kek []byte) ([]byte, error) {
	if len(kek) != KEKLen {
		return nil, fmt.Errorf("keywrap.Unwrap: kek length %d, want %d",
			len(kek), KEKLen)
	}
	if len(wrapped) < NonceLen+TagLen {
		return nil, fmt.Errorf("%w: wrapped slice too short (%d bytes)",
			errs.ErrDecryptionFailed, len(wrapped))
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("keywrap.Unwrap: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keywrap.Unwrap: gcm init: %w", err)
	}

	nonce := wrapped[:NonceLen]
	ciphertext := wrapped[NonceLen:]
	dek, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Fold all GCM errors into ErrDecryptionFailed — callers
		// must not branch on the specific cause. The wrapped
		// %w preserves the chain for debug-grade logging.
		return nil, fmt.Errorf("%w: %v", errs.ErrDecryptionFailed, err)
	}
	return dek, nil
}

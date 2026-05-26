package keyring

import (
	"crypto/rand"
	"errors"
	"fmt"

	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/errs"
)

// wrapKEK encrypts dek with kek using AES-256-GCM and a fresh nonce.
// Layout of the result: nonce | ciphertext | auth-tag.
func wrapKEK(dek, kek []byte) ([]byte, error) {
	if len(kek) != aead.DEKLen {
		return nil, fmt.Errorf("keyring: wrap: kek length %d, want %d", len(kek), aead.DEKLen)
	}
	if len(dek) == 0 {
		return nil, errors.New("keyring: wrap: empty dek")
	}

	gcm, err := aead.NewGCM(kek)
	if err != nil {
		return nil, fmt.Errorf("keyring: wrap: %w", err)
	}

	nonce := make([]byte, aead.NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keyring: wrap: nonce: %w", err)
	}

	// Pre-size so nonce ‖ ciphertext ‖ tag lands in one allocation.
	out := make([]byte, aead.NonceLen, aead.NonceLen+len(dek)+aead.TagLen)
	copy(out, nonce)
	return gcm.Seal(out, nonce, dek, nil), nil
}

// unwrapKEK reverses wrapKEK. Tamper, truncation, and wrong-key are
// folded into errs.ErrDecryptionFailed — deliberately
// indistinguishable so callers cannot branch on the cause.
func unwrapKEK(wrapped, kek []byte) ([]byte, error) {
	if len(kek) != aead.DEKLen {
		return nil, fmt.Errorf("keyring: unwrap: kek length %d, want %d", len(kek), aead.DEKLen)
	}
	if len(wrapped) < aead.NonceLen+aead.TagLen {
		return nil, fmt.Errorf("%w: wrapped slice too short (%d bytes)", errs.ErrDecryptionFailed, len(wrapped))
	}

	gcm, err := aead.NewGCM(kek)
	if err != nil {
		return nil, fmt.Errorf("keyring: unwrap: %w", err)
	}

	nonce := wrapped[:aead.NonceLen]
	ciphertext := wrapped[aead.NonceLen:]
	dek, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrDecryptionFailed, err)
	}
	return dek, nil
}

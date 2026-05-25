package aead

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// DEKLen is the required data-encryption-key length: 32 bytes for
// AES-256.
const DEKLen = 32

// NonceLen is the AES-GCM nonce (IV) size — fixed at 12 by the
// standard. Both the manifest-body wire layout and the segmented
// blob format assume this width.
const NonceLen = 12

// TagLen is the AES-GCM authentication tag size — fixed at 16.
const TagLen = 16

// NewGCM builds an AES-256-GCM cipher.AEAD from key, enforcing the
// project invariants: a 32-byte key and the standard 12-byte nonce.
// It is the one place these checks live; callers that need an
// AES-GCM primitive (manifest body crypto, the aes-gcm pipeline
// stage) go through here rather than calling crypto/aes + crypto/cipher
// directly.
func NewGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != DEKLen {
		return nil, fmt.Errorf("aead: key length %d, want %d", len(key), DEKLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aead: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aead: cipher.NewGCM: %w", err)
	}
	if gcm.NonceSize() != NonceLen {
		// Defensive: the standard library's GCM uses a 12-byte
		// nonce; assert it rather than trust a magic constant.
		return nil, fmt.Errorf("aead: unexpected nonce size %d", gcm.NonceSize())
	}
	return gcm, nil
}

package aead_test

import (
	"bytes"
	"testing"

	"scrinium.dev/engine/internal/aead"
)

// NewGCM is the AEAD construction at the root of every encrypted blob and
// manifest. These pin its contract — the key length it enforces, the nonce
// and tag sizes the rest of the engine assumes, and that seal/open actually
// authenticates (tamper, wrong AAD, and wrong key all fail closed).

func TestNewGCM_RejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, aead.DEKLen - 1, aead.DEKLen + 1, 64} {
		if _, err := aead.NewGCM(make([]byte, n)); err == nil {
			t.Errorf("NewGCM(%d-byte key): expected error, got nil", n)
		}
	}
	if _, err := aead.NewGCM(make([]byte, aead.DEKLen)); err != nil {
		t.Errorf("NewGCM(%d-byte key): unexpected error: %v", aead.DEKLen, err)
	}
}

func TestNewGCM_NonceAndTagSizes(t *testing.T) {
	gcm, err := aead.NewGCM(make([]byte, aead.DEKLen))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}
	if gcm.NonceSize() != aead.NonceLen {
		t.Errorf("NonceSize = %d, want %d", gcm.NonceSize(), aead.NonceLen)
	}
	if gcm.Overhead() != aead.TagLen {
		t.Errorf("Overhead = %d, want %d (tag)", gcm.Overhead(), aead.TagLen)
	}
}

func TestNewGCM_SealOpenRoundTrip(t *testing.T) {
	key := make([]byte, aead.DEKLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	gcm, err := aead.NewGCM(key)
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	nonce := make([]byte, aead.NonceLen)
	for i := range nonce {
		nonce[i] = byte(0xA0 + i)
	}
	plaintext := []byte("authenticated encryption round-trip")
	aad := []byte("associated-data")

	ct := gcm.Seal(nil, nonce, plaintext, aad)
	if len(ct)-len(plaintext) != aead.TagLen {
		t.Errorf("ciphertext overhead = %d, want %d (tag)", len(ct)-len(plaintext), aead.TagLen)
	}

	got, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		t.Fatalf("Open (valid): %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}

	// Tampered ciphertext must fail the tag check.
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := gcm.Open(nil, nonce, tampered, aad); err == nil {
		t.Error("Open accepted tampered ciphertext")
	}

	// Wrong AAD must fail.
	if _, err := gcm.Open(nil, nonce, ct, []byte("other-aad")); err == nil {
		t.Error("Open accepted ciphertext under wrong AAD")
	}

	// Wrong key must fail.
	otherGCM, err := aead.NewGCM(make([]byte, aead.DEKLen))
	if err != nil {
		t.Fatalf("NewGCM (other): %v", err)
	}
	if _, err := otherGCM.Open(nil, nonce, ct, aad); err == nil {
		t.Error("Open accepted ciphertext under wrong key")
	}
}

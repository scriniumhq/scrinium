package keyring

import (
	"bytes"
	"crypto/rand"
	"errors"
	"scrinium.dev/store/internal/aead"
	"testing"

	"scrinium.dev/errs"
)

// freshKEK returns 32 random bytes. Tests use it instead of
// deriveKEK to avoid the dependency — keywrap and kdf must be
// independently testable.
func freshKEK(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, aead.DEKLen)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestWrapUnwrap_RoundTrip(t *testing.T) {
	kek := freshKEK(t)
	dek := bytes.Repeat([]byte{0x42}, 32)

	wrapped, err := wrapKEK(dek, kek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	got, err := unwrapKEK(wrapped, kek)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("round-trip mismatch:\n got %x\nwant %x", got, dek)
	}
}

func TestWrap_LayoutSize(t *testing.T) {
	kek := freshKEK(t)
	dek := bytes.Repeat([]byte{0x01}, 32)

	wrapped, err := wrapKEK(dek, kek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	want := aead.NonceLen + len(dek) + aead.TagLen
	if len(wrapped) != want {
		t.Errorf("wrapped length: got %d, want %d", len(wrapped), want)
	}
}

func TestWrap_NoncesAreUnique(t *testing.T) {
	kek := freshKEK(t)
	dek := bytes.Repeat([]byte{0x01}, 32)
	w1, _ := wrapKEK(dek, kek)
	w2, _ := wrapKEK(dek, kek)
	// Same key, same DEK, but ciphertexts must diverge — proves
	// nonce randomness. If the first NonceLen bytes are equal,
	// our RNG or implementation is broken.
	if bytes.Equal(w1[:aead.NonceLen], w2[:aead.NonceLen]) {
		t.Fatal("two Wrap calls produced identical nonces")
	}
	if bytes.Equal(w1, w2) {
		t.Fatal("two Wrap calls produced identical ciphertexts")
	}
}

func TestUnwrap_WrongKEK(t *testing.T) {
	kek1 := freshKEK(t)
	kek2 := freshKEK(t)
	dek := bytes.Repeat([]byte{0x42}, 32)

	wrapped, _ := wrapKEK(dek, kek1)

	_, err := unwrapKEK(wrapped, kek2)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrap_TamperedCiphertext(t *testing.T) {
	kek := freshKEK(t)
	dek := bytes.Repeat([]byte{0x42}, 32)

	wrapped, _ := wrapKEK(dek, kek)
	// Flip one bit in the ciphertext (skip the nonce).
	wrapped[aead.NonceLen+5] ^= 0x01

	_, err := unwrapKEK(wrapped, kek)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrap_TamperedNonce(t *testing.T) {
	kek := freshKEK(t)
	dek := bytes.Repeat([]byte{0x42}, 32)

	wrapped, _ := wrapKEK(dek, kek)
	// Flip one bit in the nonce.
	wrapped[0] ^= 0x01

	_, err := unwrapKEK(wrapped, kek)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrap_TamperedTag(t *testing.T) {
	kek := freshKEK(t)
	dek := bytes.Repeat([]byte{0x42}, 32)

	wrapped, _ := wrapKEK(dek, kek)
	// Flip a bit in the last 16 bytes — the auth tag.
	wrapped[len(wrapped)-1] ^= 0x01

	_, err := unwrapKEK(wrapped, kek)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrap_TooShort(t *testing.T) {
	kek := freshKEK(t)

	for _, n := range []int{0, 1, aead.NonceLen, aead.NonceLen + aead.TagLen - 1} {
		_, err := unwrapKEK(make([]byte, n), kek)
		if !errors.Is(err, errs.ErrDecryptionFailed) {
			t.Errorf("len=%d: expected ErrDecryptionFailed, got %v", n, err)
		}
	}
}

func TestWrap_RejectsBadKEKLength(t *testing.T) {
	dek := bytes.Repeat([]byte{0x01}, 32)
	for _, n := range []int{0, 16, 24, 31, 33, 64} {
		kek := bytes.Repeat([]byte{0xAA}, n)
		if _, err := wrapKEK(dek, kek); err == nil {
			t.Errorf("Wrap accepted KEK of length %d", n)
		}
	}
}

func TestUnwrap_RejectsBadKEKLength(t *testing.T) {
	good := freshKEK(t)
	dek := bytes.Repeat([]byte{0x01}, 32)
	wrapped, _ := wrapKEK(dek, good)

	for _, n := range []int{0, 16, 24, 31, 33, 64} {
		kek := bytes.Repeat([]byte{0xAA}, n)
		if _, err := unwrapKEK(wrapped, kek); err == nil {
			t.Errorf("Unwrap accepted KEK of length %d", n)
		}
	}
}

func TestWrap_RejectsEmptyDEK(t *testing.T) {
	kek := freshKEK(t)
	if _, err := wrapKEK(nil, kek); err == nil {
		t.Error("Wrap accepted nil DEK")
	}
	if _, err := wrapKEK([]byte{}, kek); err == nil {
		t.Error("Wrap accepted empty DEK")
	}
}

// TestWrapUnwrap_VariableDEKSize sanity-checks that the wrapper
// is not specialised to 32-byte input. AES-GCM accepts any
// plaintext length; the engine wraps a 32-byte DEK in practice,
// but locking that in here would make a future format change
// noisier than necessary.
func TestWrapUnwrap_VariableDEKSize(t *testing.T) {
	kek := freshKEK(t)
	for _, n := range []int{1, 16, 32, 64, 128, 1024} {
		dek := bytes.Repeat([]byte{byte(n)}, n)
		wrapped, err := wrapKEK(dek, kek)
		if err != nil {
			t.Fatalf("len=%d: Wrap: %v", n, err)
		}
		got, err := unwrapKEK(wrapped, kek)
		if err != nil {
			t.Fatalf("len=%d: Unwrap: %v", n, err)
		}
		if !bytes.Equal(got, dek) {
			t.Errorf("len=%d: round-trip mismatch", n)
		}
	}
}

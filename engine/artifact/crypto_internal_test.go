package artifact

import (
	"bytes"
	"crypto/rand"
	"errors"
	"slices"
	"testing"

	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/errs"
)

func freshDEK(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, aead.DEKLen)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return b
}

func TestSealOpen_RoundTrip(t *testing.T) {
	dek := freshDEK(t)
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	aad := []byte("manifest-header-bytes")

	ciphertext, err := sealBody(plaintext, dek, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := openBody(ciphertext, dek, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("plaintext: got %q, want %q", got, plaintext)
	}
}

func TestSealOpen_EmptyPlaintext(t *testing.T) {
	dek := freshDEK(t)
	got, err := sealBody(nil, dek, []byte("aad"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(got) != aead.NonceLen+aead.TagLen {
		t.Errorf("ciphertext length: got %d, want %d", len(got), aead.NonceLen+aead.TagLen)
	}
	plain, err := openBody(got, dek, []byte("aad"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(plain) != 0 {
		t.Errorf("plaintext should be empty, got %d bytes", len(plain))
	}
}

func TestSealOpen_EmptyAAD(t *testing.T) {
	dek := freshDEK(t)
	plaintext := []byte("body")
	ciphertext, err := sealBody(plaintext, dek, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := openBody(ciphertext, dek, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Error("round-trip with nil AAD changed plaintext")
	}
}

func TestSeal_OutputIsNondeterministic(t *testing.T) {
	dek := freshDEK(t)
	a, _ := sealBody([]byte("same"), dek, []byte("same"))
	b, _ := sealBody([]byte("same"), dek, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("Seal must produce a fresh nonce per call")
	}
}

func TestSeal_NonceIsPrependedAndUnique(t *testing.T) {
	dek := freshDEK(t)
	a, _ := sealBody([]byte("x"), dek, nil)
	b, _ := sealBody([]byte("x"), dek, nil)
	if bytes.Equal(a[:aead.NonceLen], b[:aead.NonceLen]) {
		t.Error("two Seal calls produced identical nonces")
	}
}

func TestSeal_RejectsWrongDEKLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		_, err := sealBody([]byte("x"), make([]byte, n), nil)
		if err == nil {
			t.Errorf("DEK length %d: expected error", n)
		}
	}
}

func TestOpen_RejectsWrongDEKLength(t *testing.T) {
	dek := freshDEK(t)
	good, _ := sealBody([]byte("x"), dek, nil)
	for _, n := range []int{0, 16, 31, 33, 64} {
		_, err := openBody(good, make([]byte, n), nil)
		if err == nil {
			t.Errorf("DEK length %d: expected error", n)
		}
	}
}

func TestOpen_WrongDEK(t *testing.T) {
	dekA := freshDEK(t)
	dekB := freshDEK(t)
	ciphertext, _ := sealBody([]byte("plaintext"), dekA, nil)

	_, err := openBody(ciphertext, dekB, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TamperedCiphertext(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := sealBody([]byte("plaintext"), dek, nil)

	tampered := slices.Clone(ciphertext)
	tampered[aead.NonceLen+1] ^= 0x01

	_, err := openBody(tampered, dek, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TamperedNonce(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := sealBody([]byte("plaintext"), dek, nil)

	tampered := slices.Clone(ciphertext)
	tampered[3] ^= 0x01

	_, err := openBody(tampered, dek, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TamperedAAD(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := sealBody([]byte("plaintext"), dek, []byte("header-v1"))

	_, err := openBody(ciphertext, dek, []byte("header-v2"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_MissingAADWhenSealedWithAAD(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := sealBody([]byte("plaintext"), dek, []byte("aad"))

	_, err := openBody(ciphertext, dek, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TooShort(t *testing.T) {
	dek := freshDEK(t)
	for _, n := range []int{0, 1, 11, minCiphertext - 1} {
		_, err := openBody(make([]byte, n), dek, nil)
		if err == nil {
			t.Errorf("ciphertext length %d: expected truncation error", n)
		}
		if errors.Is(err, errs.ErrDecryptionFailed) {
			t.Errorf("ciphertext length %d: should not surface ErrDecryptionFailed for truncation", n)
		}
	}
}

func TestSeal_OutputLayout(t *testing.T) {
	dek := freshDEK(t)
	plaintext := []byte("hello") // 5 bytes
	got, err := sealBody(plaintext, dek, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantLen := aead.NonceLen + len(plaintext) + aead.TagLen
	if len(got) != wantLen {
		t.Errorf("layout: got len=%d, want %d", len(got), wantLen)
	}
}

func FuzzSealOpen_RoundTrip(f *testing.F) {
	f.Add([]byte("hello"), []byte("aad"))
	f.Add([]byte{}, []byte{})
	f.Add(bytes.Repeat([]byte{0xFF}, 1024), []byte("header"))

	f.Fuzz(func(t *testing.T, plaintext, aad []byte) {
		dek := make([]byte, aead.DEKLen)
		for i := range dek {
			dek[i] = byte(i)
		}

		ciphertext, err := sealBody(plaintext, dek, aad)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		got, err := openBody(ciphertext, dek, aad)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("round-trip mismatch:\n got %x\nwant %x", got, plaintext)
		}
	})
}

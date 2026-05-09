package manifestcrypto_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/manifestcrypto"
)

// freshDEK returns a 32-byte DEK from crypto/rand. Each test
// gets its own — round-trip must work on any DEK, not a fixed
// one.
func freshDEK(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, manifestcrypto.DEKLen)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return b
}

// --- Round-trip ---

func TestSealOpen_RoundTrip(t *testing.T) {
	dek := freshDEK(t)
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	aad := []byte("manifest-header-bytes")

	ciphertext, err := manifestcrypto.Seal(plaintext, dek, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := manifestcrypto.Open(ciphertext, dek, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("plaintext: got %q, want %q", got, plaintext)
	}
}

func TestSealOpen_EmptyPlaintext(t *testing.T) {
	dek := freshDEK(t)
	got, err := manifestcrypto.Seal(nil, dek, []byte("aad"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Empty plaintext + nonce(12) + tag(16) = 28 bytes minimum.
	if len(got) != 12+16 {
		t.Errorf("ciphertext length: got %d, want 28", len(got))
	}
	plain, err := manifestcrypto.Open(got, dek, []byte("aad"))
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
	ciphertext, err := manifestcrypto.Seal(plaintext, dek, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := manifestcrypto.Open(ciphertext, dek, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Error("round-trip with nil AAD changed plaintext")
	}
}

func TestSeal_OutputIsNondeterministic(t *testing.T) {
	dek := freshDEK(t)
	a, _ := manifestcrypto.Seal([]byte("same"), dek, []byte("same"))
	b, _ := manifestcrypto.Seal([]byte("same"), dek, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("Seal must produce a fresh nonce per call; identical output suggests nonce reuse")
	}
}

func TestSeal_NonceIsPrependedAndUnique(t *testing.T) {
	dek := freshDEK(t)
	a, _ := manifestcrypto.Seal([]byte("x"), dek, nil)
	b, _ := manifestcrypto.Seal([]byte("x"), dek, nil)
	// First 12 bytes are the nonce — must differ between calls.
	if bytes.Equal(a[:12], b[:12]) {
		t.Error("two Seal calls produced identical nonces")
	}
}

// --- DEK validation ---

func TestSeal_RejectsWrongDEKLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		dek := make([]byte, n)
		_, err := manifestcrypto.Seal([]byte("x"), dek, nil)
		if err == nil {
			t.Errorf("DEK length %d: expected error", n)
		}
	}
}

func TestOpen_RejectsWrongDEKLength(t *testing.T) {
	dek := freshDEK(t)
	good, _ := manifestcrypto.Seal([]byte("x"), dek, nil)
	for _, n := range []int{0, 16, 31, 33, 64} {
		bad := make([]byte, n)
		_, err := manifestcrypto.Open(good, bad, nil)
		if err == nil {
			t.Errorf("DEK length %d: expected error", n)
		}
	}
}

// --- Tamper detection ---

func TestOpen_WrongDEK(t *testing.T) {
	dekA := freshDEK(t)
	dekB := freshDEK(t)
	ciphertext, _ := manifestcrypto.Seal([]byte("plaintext"), dekA, nil)

	_, err := manifestcrypto.Open(ciphertext, dekB, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TamperedCiphertext(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := manifestcrypto.Seal([]byte("plaintext"), dek, nil)

	// Flip a bit in the body (after the nonce).
	tampered := append([]byte{}, ciphertext...)
	tampered[15] ^= 0x01

	_, err := manifestcrypto.Open(tampered, dek, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TamperedNonce(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := manifestcrypto.Seal([]byte("plaintext"), dek, nil)

	// Flip a bit in the nonce.
	tampered := append([]byte{}, ciphertext...)
	tampered[3] ^= 0x01

	_, err := manifestcrypto.Open(tampered, dek, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_TamperedAAD(t *testing.T) {
	dek := freshDEK(t)
	aad := []byte("header-v1")
	ciphertext, _ := manifestcrypto.Seal([]byte("plaintext"), dek, aad)

	// Open with different AAD — auth tag verification fails
	// even though DEK and ciphertext are correct.
	_, err := manifestcrypto.Open(ciphertext, dek, []byte("header-v2"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestOpen_MissingAADWhenSealedWithAAD(t *testing.T) {
	dek := freshDEK(t)
	ciphertext, _ := manifestcrypto.Seal([]byte("plaintext"), dek, []byte("aad"))

	_, err := manifestcrypto.Open(ciphertext, dek, nil)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

// --- Truncated ciphertext ---

func TestOpen_TooShort(t *testing.T) {
	dek := freshDEK(t)
	for _, n := range []int{0, 1, 11, 27} {
		bad := make([]byte, n)
		_, err := manifestcrypto.Open(bad, dek, nil)
		if err == nil {
			t.Errorf("ciphertext length %d: expected truncation error", n)
		}
		// Truncation is NOT ErrDecryptionFailed — the data is
		// structurally invalid before decryption is even
		// attempted.
		if errors.Is(err, errs.ErrDecryptionFailed) {
			t.Errorf("ciphertext length %d: should not surface ErrDecryptionFailed for truncation",
				n)
		}
	}
}

// --- Layout: nonce | ciphertext | tag ---

func TestSeal_OutputLayout(t *testing.T) {
	dek := freshDEK(t)
	plaintext := []byte("hello") // 5 bytes
	got, err := manifestcrypto.Seal(plaintext, dek, nil)
	if err != nil {
		t.Fatal(err)
	}
	// nonce(12) + plaintext(5) + tag(16) = 33 bytes.
	if len(got) != 12+5+16 {
		t.Errorf("layout: got len=%d, want %d", len(got), 12+5+16)
	}
}

// --- Fuzz: AEAD round-trip ---

func FuzzSealOpen_RoundTrip(f *testing.F) {
	// Seeds: simple cases, edges.
	f.Add([]byte("hello"), []byte("aad"))
	f.Add([]byte{}, []byte{})
	f.Add(bytes.Repeat([]byte{0xFF}, 1024), []byte("header"))

	f.Fuzz(func(t *testing.T, plaintext, aad []byte) {
		// Fixed DEK for the fuzzer — no need to also fuzz the
		// key, AES-GCM correctness is established stdlib.
		dek := make([]byte, manifestcrypto.DEKLen)
		for i := range dek {
			dek[i] = byte(i)
		}

		ciphertext, err := manifestcrypto.Seal(plaintext, dek, aad)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		got, err := manifestcrypto.Open(ciphertext, dek, aad)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("round-trip mismatch:\n got %x\nwant %x", got, plaintext)
		}
	})
}

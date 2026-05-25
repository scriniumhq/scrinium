package keyring

import (
	"bytes"
	"errors"
	"scrinium.dev/engine/internal/aead"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

func TestGenerateDEK_LengthAndUniqueness(t *testing.T) {
	a, err := GenerateDEK()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != aead.DEKLen {
		t.Fatalf("len: got %d, want %d", len(a), aead.DEKLen)
	}
	b, _ := GenerateDEK()
	if bytes.Equal(a, b) {
		t.Fatal("two GenerateDEK calls returned identical bytes")
	}
}

func TestWrapUnwrapDEK_RoundTrip(t *testing.T) {
	dek, _ := GenerateDEK()
	pass := []byte("correct horse battery staple")

	wrapped, params, err := WrapDEK(dek, pass, domain.KDFParams{})
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if params.Algorithm != kdfAlgorithm {
		t.Errorf("Algorithm: got %q, want %q", params.Algorithm, kdfAlgorithm)
	}
	if len(params.Salt) != saltLen {
		t.Errorf("Salt length: got %d, want %d", len(params.Salt), saltLen)
	}

	got, err := UnwrapDEK(wrapped, params, pass)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("round-trip changed the DEK")
	}
}

func TestWrapDEK_UsesProvidedCost(t *testing.T) {
	dek, _ := GenerateDEK()
	pass := []byte("xx")
	cost := domain.KDFParams{Time: 2, Memory: 32 * 1024, Threads: 2}

	_, params, err := WrapDEK(dek, pass, cost)
	if err != nil {
		t.Fatal(err)
	}
	if params.Time != 2 || params.Memory != 32*1024 || params.Threads != 2 {
		t.Errorf("cost not propagated: got %+v", params)
	}
}

func TestWrapDEK_DefaultsZeroCost(t *testing.T) {
	dek, _ := GenerateDEK()
	pass := []byte("xx")
	_, params, err := WrapDEK(dek, pass, domain.KDFParams{})
	if err != nil {
		t.Fatal(err)
	}
	d := DefaultKDFParams()
	if params.Time != d.Time || params.Memory != d.Memory || params.Threads != d.Threads {
		t.Errorf("default cost not applied: got %+v, want %+v", params, d)
	}
}

func TestWrapDEK_RejectsBadCost(t *testing.T) {
	dek, _ := GenerateDEK()
	pass := []byte("xx")
	_, _, err := WrapDEK(dek, pass, domain.KDFParams{Time: 0, Memory: 64 * 1024, Threads: 4})
	if !errors.Is(err, errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams, got %v", err)
	}
}

func TestWrapDEK_RejectsBadDEKLength(t *testing.T) {
	pass := []byte("xx")
	_, _, err := WrapDEK([]byte{1, 2, 3}, pass, domain.KDFParams{})
	if err == nil {
		t.Fatal("expected error on short DEK")
	}
}

func TestWrapDEK_RejectsEmptyPassphrase(t *testing.T) {
	dek, _ := GenerateDEK()
	_, _, err := WrapDEK(dek, nil, domain.KDFParams{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
	_, _, err = WrapDEK(dek, []byte{}, domain.KDFParams{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired on empty slice, got %v", err)
	}
}

func TestUnwrapDEK_WrongPassphrase(t *testing.T) {
	dek, _ := GenerateDEK()
	wrapped, params, _ := WrapDEK(dek, []byte("right"), domain.KDFParams{})

	_, err := UnwrapDEK(wrapped, params, []byte("wrong"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrapDEK_TamperedCiphertext(t *testing.T) {
	dek, _ := GenerateDEK()
	wrapped, params, _ := WrapDEK(dek, []byte("pass"), domain.KDFParams{})

	tampered := append([]byte{}, wrapped...)
	tampered[len(tampered)-1] ^= 0x01

	_, err := UnwrapDEK(tampered, params, []byte("pass"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrapDEK_TamperedSalt(t *testing.T) {
	dek, _ := GenerateDEK()
	wrapped, params, _ := WrapDEK(dek, []byte("pass"), domain.KDFParams{})

	// Tampering the salt produces a different KEK; unwrap fails
	// with ErrDecryptionFailed (folded from GCM tag mismatch).
	params.Salt[0] ^= 0x01
	_, err := UnwrapDEK(wrapped, params, []byte("pass"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrapDEK_BadAlgorithm(t *testing.T) {
	dek, _ := GenerateDEK()
	wrapped, params, _ := WrapDEK(dek, []byte("pass"), domain.KDFParams{})
	params.Algorithm = "scrypt"
	_, err := UnwrapDEK(wrapped, params, []byte("pass"))
	if !errors.Is(err, errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams, got %v", err)
	}
}

func TestUnwrapDEK_EmptyPassphrase(t *testing.T) {
	dek, _ := GenerateDEK()
	wrapped, params, _ := WrapDEK(dek, []byte("pass"), domain.KDFParams{})
	_, err := UnwrapDEK(wrapped, params, nil)
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

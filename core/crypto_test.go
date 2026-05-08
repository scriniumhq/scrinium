package core

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/rkurbatov/scrinium/core/internal/kdf"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

func TestGenerateDEK_LengthAndUniqueness(t *testing.T) {
	a, err := generateDEK()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != dekLen {
		t.Fatalf("len: got %d, want %d", len(a), dekLen)
	}
	b, _ := generateDEK()
	if bytes.Equal(a, b) {
		t.Fatal("two generateDEK calls returned identical bytes")
	}
}

func TestWrapUnwrapDEK_RoundTrip(t *testing.T) {
	dek, _ := generateDEK()
	pass := []byte("correct horse battery staple")

	wrapped, params, err := wrapDEK(dek, pass, domain.KDFParams{})
	if err != nil {
		t.Fatalf("wrapDEK: %v", err)
	}
	if params.Algorithm != kdf.Algorithm {
		t.Errorf("Algorithm: got %q, want %q", params.Algorithm, kdf.Algorithm)
	}
	if len(params.Salt) != kdf.SaltLen {
		t.Errorf("Salt length: got %d, want %d", len(params.Salt), kdf.SaltLen)
	}

	got, err := unwrapDEK(wrapped, params, pass)
	if err != nil {
		t.Fatalf("unwrapDEK: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("round-trip changed the DEK")
	}
}

func TestWrapDEK_UsesProvidedCost(t *testing.T) {
	dek, _ := generateDEK()
	pass := []byte("xx")
	cost := domain.KDFParams{Time: 2, Memory: 32 * 1024, Threads: 2}

	_, params, err := wrapDEK(dek, pass, cost)
	if err != nil {
		t.Fatal(err)
	}
	if params.Time != 2 || params.Memory != 32*1024 || params.Threads != 2 {
		t.Errorf("cost not propagated: got %+v", params)
	}
}

func TestWrapDEK_DefaultsZeroCost(t *testing.T) {
	dek, _ := generateDEK()
	pass := []byte("xx")
	_, params, err := wrapDEK(dek, pass, domain.KDFParams{})
	if err != nil {
		t.Fatal(err)
	}
	d := kdf.Default()
	if params.Time != d.Time || params.Memory != d.Memory || params.Threads != d.Threads {
		t.Errorf("default cost not applied: got %+v, want %+v", params, d)
	}
}

func TestWrapDEK_RejectsBadCost(t *testing.T) {
	dek, _ := generateDEK()
	pass := []byte("xx")
	_, _, err := wrapDEK(dek, pass, domain.KDFParams{Time: 0, Memory: 64 * 1024, Threads: 4})
	if !errors.Is(err, errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams, got %v", err)
	}
}

func TestWrapDEK_RejectsBadDEKLength(t *testing.T) {
	pass := []byte("xx")
	_, _, err := wrapDEK([]byte{1, 2, 3}, pass, domain.KDFParams{})
	if err == nil {
		t.Fatal("expected error on short DEK")
	}
}

func TestWrapDEK_RejectsEmptyPassphrase(t *testing.T) {
	dek, _ := generateDEK()
	_, _, err := wrapDEK(dek, nil, domain.KDFParams{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
	_, _, err = wrapDEK(dek, []byte{}, domain.KDFParams{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired on empty slice, got %v", err)
	}
}

func TestUnwrapDEK_WrongPassphrase(t *testing.T) {
	dek, _ := generateDEK()
	wrapped, params, _ := wrapDEK(dek, []byte("right"), domain.KDFParams{})

	_, err := unwrapDEK(wrapped, params, []byte("wrong"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrapDEK_TamperedCiphertext(t *testing.T) {
	dek, _ := generateDEK()
	wrapped, params, _ := wrapDEK(dek, []byte("pass"), domain.KDFParams{})

	tampered := append([]byte{}, wrapped...)
	tampered[len(tampered)-1] ^= 0x01

	_, err := unwrapDEK(tampered, params, []byte("pass"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrapDEK_TamperedSalt(t *testing.T) {
	dek, _ := generateDEK()
	wrapped, params, _ := wrapDEK(dek, []byte("pass"), domain.KDFParams{})

	// Tampering the salt produces a different KEK; unwrap fails
	// with ErrDecryptionFailed (folded from GCM tag mismatch).
	params.Salt[0] ^= 0x01
	_, err := unwrapDEK(wrapped, params, []byte("pass"))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestUnwrapDEK_BadAlgorithm(t *testing.T) {
	dek, _ := generateDEK()
	wrapped, params, _ := wrapDEK(dek, []byte("pass"), domain.KDFParams{})
	params.Algorithm = "scrypt"
	_, err := unwrapDEK(wrapped, params, []byte("pass"))
	if !errors.Is(err, errs.ErrInvalidKDFParams) {
		t.Fatalf("expected ErrInvalidKDFParams, got %v", err)
	}
}

func TestUnwrapDEK_EmptyPassphrase(t *testing.T) {
	dek, _ := generateDEK()
	wrapped, params, _ := wrapDEK(dek, []byte("pass"), domain.KDFParams{})
	_, err := unwrapDEK(wrapped, params, nil)
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

// --- callProvider ---

func TestCallProvider_NilProvider(t *testing.T) {
	_, err := callProvider(context.Background(), nil, PassphraseHint{Reason: "init"})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestCallProvider_ReturnsPassphrase(t *testing.T) {
	p := PassphraseProvider(func(_ context.Context, h PassphraseHint) ([]byte, error) {
		if h.Reason != "unlock" {
			t.Errorf("Reason: got %q, want unlock", h.Reason)
		}
		return []byte("hello"), nil
	})
	got, err := callProvider(context.Background(), p, PassphraseHint{Reason: "unlock"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("passphrase: got %q, want hello", got)
	}
}

func TestCallProvider_PropagatesError(t *testing.T) {
	sentinel := errors.New("user cancelled")
	p := PassphraseProvider(func(_ context.Context, _ PassphraseHint) ([]byte, error) {
		return nil, sentinel
	})
	_, err := callProvider(context.Background(), p, PassphraseHint{})
	if !errors.Is(err, errs.ErrPassphraseProvider) {
		t.Fatalf("expected ErrPassphraseProvider, got %v", err)
	}
	// Underlying cause preserved through %w.
	if !errors.Is(err, sentinel) {
		// fmt.Errorf("%w: %v", ...) only wraps the first %w —
		// sentinel is %v, so errors.Is doesn't reach it. Test
		// via string instead.
		if !bytes.Contains([]byte(err.Error()), []byte("user cancelled")) {
			t.Errorf("error should mention underlying cause: %v", err)
		}
	}
}

func TestCallProvider_RejectsEmptyPassphrase(t *testing.T) {
	p := PassphraseProvider(func(_ context.Context, _ PassphraseHint) ([]byte, error) {
		return []byte{}, nil
	})
	_, err := callProvider(context.Background(), p, PassphraseHint{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}

	p = PassphraseProvider(func(_ context.Context, _ PassphraseHint) ([]byte, error) {
		return nil, nil
	})
	_, err = callProvider(context.Background(), p, PassphraseHint{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired on nil from provider, got %v", err)
	}
}

// --- zeroBytes ---

func TestZeroBytes(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	wipeSecret(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte %d: got %d, want 0", i, v)
		}
	}

	// Nil and empty are no-op safe.
	wipeSecret(nil)
	wipeSecret([]byte{})
}

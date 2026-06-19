package crypto

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

func TestCallProvider_NilProvider(t *testing.T) {
	_, err := CallProvider(context.Background(), nil, domain.PassphraseHint{Reason: "init"})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestCallProvider_ReturnsPassphrase(t *testing.T) {
	p := domain.PassphraseProvider(func(_ context.Context, h domain.PassphraseHint) ([]byte, error) {
		if h.Reason != "unlock" {
			t.Errorf("Reason: got %q, want unlock", h.Reason)
		}
		return []byte("hello"), nil
	})
	got, err := CallProvider(context.Background(), p, domain.PassphraseHint{Reason: "unlock"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("passphrase: got %q, want hello", got)
	}
}

func TestCallProvider_PropagatesError(t *testing.T) {
	sentinel := errors.New("user cancelled")
	p := domain.PassphraseProvider(func(_ context.Context, _ domain.PassphraseHint) ([]byte, error) {
		return nil, sentinel
	})
	_, err := CallProvider(context.Background(), p, domain.PassphraseHint{})
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
	p := domain.PassphraseProvider(func(_ context.Context, _ domain.PassphraseHint) ([]byte, error) {
		return []byte{}, nil
	})
	_, err := CallProvider(context.Background(), p, domain.PassphraseHint{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}

	p = func(_ context.Context, _ domain.PassphraseHint) ([]byte, error) {
		return nil, nil
	}
	_, err = CallProvider(context.Background(), p, domain.PassphraseHint{})
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired on nil from provider, got %v", err)
	}
}

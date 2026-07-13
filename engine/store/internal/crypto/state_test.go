package crypto

import (
	"bytes"
	"errors"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/errs"
)

// allZero reports whether every byte of b is zero. Used to assert that
// secret material is scrubbed, not merely dropped.
func allZero(b []byte) bool {
	return bytes.Equal(b, make([]byte, len(b)))
}

// TestCloseSecrets_WipesAndClearsDEK is the unit-level counterpart of the
// store's TestClose_WipesDEK: it verifies CloseSecrets both zeroes the
// backing array (so the key does not linger in freed memory) and clears
// the field. The store test only asserts the observable presence bit; the
// byte-level scrub is checked here, where the field is reachable.
func TestCloseSecrets_WipesAndClearsDEK(t *testing.T) {
	s := New(nil, []byte{1, 2, 3, 4, 5, 6, 7, 8}, nil, nil, nil)
	ref := s.dek // same backing array; observe its bytes after the wipe

	resolver := s.CloseSecrets()
	if resolver != nil {
		t.Errorf("CloseSecrets resolver: want nil (none installed), got %v", resolver)
	}
	if s.dek != nil {
		t.Errorf("dek: want nil after CloseSecrets, got %v", s.dek)
	}
	if !allZero(ref) {
		t.Errorf("dek backing array: want zeroed after CloseSecrets, got %v", ref)
	}
}

// TestWipeDEK_WipesAndClearsDEK covers the unlock-failure path's WipeDEK
// the same way.
func TestWipeDEK_WipesAndClearsDEK(t *testing.T) {
	s := New(nil, []byte{9, 8, 7, 6}, nil, nil, nil)
	ref := s.dek

	s.WipeDEK()
	if s.dek != nil {
		t.Errorf("dek: want nil after WipeDEK, got %v", s.dek)
	}
	if !allZero(ref) {
		t.Errorf("dek backing array: want zeroed after WipeDEK, got %v", ref)
	}
}

// TestCloseSecrets_NilDEK_NoPanic guards the empty/locked-Store path.
func TestCloseSecrets_NilDEK_NoPanic(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	if r := s.CloseSecrets(); r != nil {
		t.Errorf("resolver: want nil, got %v", r)
	}
	if s.dek != nil {
		t.Errorf("dek: want nil, got %v", s.dek)
	}
}

// TestHasDEK reports presence, never material.
func TestHasDEK(t *testing.T) {
	if got := New(nil, []byte{1}, nil, nil, nil).HasDEK(); !got {
		t.Error("HasDEK with a DEK: want true")
	}
	if got := New(nil, nil, nil, nil, nil).HasDEK(); got {
		t.Error("HasDEK with nil DEK: want false")
	}
	if got := New(nil, []byte{}, nil, nil, nil).HasDEK(); got {
		t.Error("HasDEK with empty DEK: want false")
	}
}

// --- DEKForWrite: preconditions and copy semantics -----------------------

func TestDEKForWrite_LockedReturnsErrLocked(t *testing.T) {
	// No DEK == Locked: an encrypting write must be refused with ErrLocked.
	_, err := New(nil, nil, nil, nil, nil).DEKForWrite(config.ManifestCryptoSealed)
	if !errors.Is(err, errs.ErrLocked) {
		t.Fatalf("DEKForWrite while locked: got %v, want ErrLocked", err)
	}
}

func TestDEKForWrite_NoResolverRejected(t *testing.T) {
	// DEK present but no resolver: an encrypting write still needs one.
	// Distinct from the locked case — this is NOT an ErrLocked.
	_, err := New(nil, []byte{1, 2, 3, 4}, nil, nil, nil).DEKForWrite(config.ManifestCryptoSealed)
	if err == nil {
		t.Fatal("DEKForWrite without a resolver should error")
	}
	if errors.Is(err, errs.ErrLocked) {
		t.Error("missing-resolver error must not be ErrLocked")
	}
}

func TestDEKForWrite_ReturnsPrivateCopy(t *testing.T) {
	dek := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	s := New(nil, dek, nil, pipeline.NewStaticKeyResolver(dek), nil)

	got, err := s.DEKForWrite(config.ManifestCryptoSealed)
	if err != nil {
		t.Fatalf("DEKForWrite: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("DEKForWrite: got %v, want %v", got, dek)
	}
	// The caller owns and wipes this copy; mutating it must not touch the
	// State's DEK, or one writer's wipe would blind the next.
	got[0] ^= 0xFF
	if s.dek[0] == got[0] {
		t.Error("DEKForWrite handed out the internal buffer, not a private copy")
	}
}

// --- asKeyProvider: the typed-nil guard ----------------------------------

func TestAsKeyProvider_NilResolverYieldsNilProvider(t *testing.T) {
	// A Locked Store's Resolver() is a nil interface; asKeyProvider must
	// forward that as a nil provider so the codec yields ErrKeyNotFound,
	// not a non-nil provider wrapping nothing.
	if p := asKeyProvider(nil); p != nil {
		t.Errorf("asKeyProvider(nil): got %v, want nil", p)
	}
	// Same guarantee through the public accessor on a resolver-less State.
	if p := New(nil, []byte{1}, nil, nil, nil).KeyProvider(); p != nil {
		t.Errorf("KeyProvider() with no resolver: got %v, want nil", p)
	}
}

func TestAsKeyProvider_NonNilForwards(t *testing.T) {
	r := pipeline.NewStaticKeyResolver([]byte{1, 2, 3, 4})
	if p := asKeyProvider(r); p == nil {
		t.Error("asKeyProvider(non-nil): got nil, want the resolver forwarded")
	}
}

package store

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/errs"
)

// --- Test scaffolding ---

// fakeKeyResolver is a custom KeyResolver: it MUST NOT be touched
// by Close. The test uses it to verify that Close does not call
// methods on host-supplied resolvers.
type fakeKeyResolver struct {
	closed atomic.Bool
}

func (r *fakeKeyResolver) GetKeys(string) ([][]byte, error)           { return nil, nil }
func (r *fakeKeyResolver) ResolveWriteKey(pipeline.KeyContext) string { return "" }
func (r *fakeKeyResolver) close()                                     { r.closed.Store(true) }

// newTestStore builds a minimal *store sufficient to exercise
// Close. It does not stand up the Driver, Index, descriptor, or
// publisher — Close does not touch any of them.
func newTestStore() *store {
	return newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		},
	})
}

// --- Idempotency ---

func TestClose_Idempotent(t *testing.T) {
	s := newTestStore()

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
}

// --- Secret wiping ---

func TestClose_WipesDEK(t *testing.T) {
	s := newTestStore()
	original := append([]byte(nil), s.dataFacet.core.crypto.dek...) // capture for length check
	dekRef := s.dataFacet.core.crypto.dek                           // grab the slice header — observe its bytes after Close

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if s.dataFacet.core.crypto.dek != nil {
		t.Errorf("s.crypto.dek: want nil after Close, got %v", s.dataFacet.core.crypto.dek)
	}
	if !allZero(dekRef) {
		t.Errorf("dek bytes should be zeroed, got %v (was %v)", dekRef, original)
	}
}

// --- Edge cases: nil/empty fields ---

func TestClose_NilDEK_NoPanic(t *testing.T) {
	s := newStore(&core{
		state: domain.StateLocked,
		crypto: cryptoState{
			dek: nil,
		},
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil dek: %v", err)
	}
}

func TestClose_EmptyDEK_NoPanic(t *testing.T) {
	s := newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek: []byte{},
		},
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close on empty dek: %v", err)
	}
}

func TestClose_NoCapabilityToken_NoPanic(t *testing.T) {
	s := newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek: []byte{1, 2, 3},
		},
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil token: %v", err)
	}
}

// --- State transition ---

// --- Closed flag ---

// TestClose_MarksClosed verifies Close sets s.closed regardless
// of the prior state. Close does NOT transition state itself —
// "closed" is its own terminal condition (surfaced as os.ErrClosed
// by checkOperational), distinct from StateLocked which means
// "encrypted store, awaiting Unlock".
func TestClose_MarksClosed(t *testing.T) {
	cases := []struct {
		name string
		from domain.StoreState
	}{
		{"FromUnlocked", domain.StateUnlocked},
		{"FromLocked", domain.StateLocked},
		{"FromBootstrapping", domain.StateBootstrapping},
		{"FromDegraded", domain.StateDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			s.dataFacet.core.state = tc.from

			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !s.dataFacet.core.closed {
				t.Errorf("s.closed: want true after Close")
			}
		})
	}
}

// TestClose_OperationsReturnErrClosed verifies the new
// post-Close behaviour: any operation gated on checkOperational
// returns os.ErrClosed, not ErrLocked. Plain (unencrypted) stores
// previously returned ErrLocked here, which sent users hunting
// for a passphrase that did not exist.
func TestClose_OperationsReturnErrClosed(t *testing.T) {
	cases := []struct {
		name string
		from domain.StoreState
	}{
		{"PlainStore", domain.StateUnlocked},
		{"EncryptedStoreUnlocked", domain.StateUnlocked},
		{"EncryptedStoreStillLocked", domain.StateLocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			s.dataFacet.core.state = tc.from

			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			err := s.dataFacet.core.checkOperational()
			if !errors.Is(err, os.ErrClosed) {
				t.Errorf("checkOperational after Close: got %v, want os.ErrClosed", err)
			}
			if errors.Is(err, errs.ErrLocked) {
				t.Errorf("checkOperational after Close: returns ErrLocked; " +
					"that should be reserved for pre-Unlock encrypted stores")
			}
		})
	}
}

// --- KeyResolver handling ---

func TestClose_DefaultStaticKeyResolver_GetsClosed(t *testing.T) {
	dek := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	resolver := pipeline.NewStaticKeyResolver(dek)

	s := newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek:         append([]byte(nil), dek...),
			keyResolver: resolver,
		},
	})

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close the default resolver is closed: GetKeys returns
	// (nil, nil) — the codec turns that into ErrKeyNotFound. (The
	// internal DEK-wipe is unit-tested in the plugins package.)
	keys, err := resolver.GetKeys("any")
	if err != nil {
		t.Errorf("GetKeys after close: unexpected err: %v", err)
	}
	if keys != nil {
		t.Errorf("GetKeys after close: want nil keys, got %v", keys)
	}
}

func TestClose_CustomKeyResolver_NotTouched(t *testing.T) {
	resolver := &fakeKeyResolver{}
	s := newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek:         []byte{1, 2, 3},
			keyResolver: resolver,
		},
	})

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if resolver.closed.Load() {
		t.Errorf("custom resolver.close() was called — host-owned resolvers must not be touched")
	}
}

func TestClose_NilKeyResolver_NoPanic(t *testing.T) {
	s := newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek:         []byte{1, 2, 3},
			keyResolver: nil,
		},
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- Concurrency ---

// TestClose_RaceWithGetKeys verifies that concurrent GetKeys on
// the default resolver does not race with Close. Run with -race
// to catch ordering bugs.
func TestClose_RaceWithGetKeys(t *testing.T) {
	dek := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	resolver := pipeline.NewStaticKeyResolver(dek)

	s := newStore(&core{
		state: domain.StateUnlocked,
		crypto: cryptoState{
			dek:         append([]byte(nil), dek...),
			keyResolver: resolver,
		},
	})

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N + 1)

	// Many concurrent GetKeys on the resolver.
	for range N {
		go func() {
			defer wg.Done()
			_, _ = resolver.GetKeys("any")
		}()
	}

	// One Close.
	go func() {
		defer wg.Done()
		_ = s.Close()
	}()

	wg.Wait()

	// After everyone is done, GetKeys must return (nil, nil).
	keys, err := resolver.GetKeys("any")
	if err != nil {
		t.Errorf("GetKeys post-close: %v", err)
	}
	if keys != nil {
		t.Errorf("GetKeys post-close: want nil, got %v", keys)
	}
}

// TestClose_RaceWithItself verifies concurrent Close calls don't
// race or double-wipe.
func TestClose_RaceWithItself(t *testing.T) {
	s := newTestStore()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			_ = s.Close()
		}()
	}
	wg.Wait()

	if !s.dataFacet.core.closed {
		t.Errorf("s.closed: want true after concurrent Closes")
	}
	if s.dataFacet.core.crypto.dek != nil {
		t.Errorf("s.crypto.dek: want nil after concurrent Closes, got %v", s.dataFacet.core.crypto.dek)
	}
}

// --- Helpers ---

func allZero(b []byte) bool {
	zero := make([]byte, len(b))
	return bytes.Equal(b, zero)
}

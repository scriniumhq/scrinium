package store

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/crypto"
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
	return &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, []byte{1, 2, 3, 4, 5, 6, 7, 8}, nil, nil, nil),
	}
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
	if !StoreHasDEK(s) {
		t.Fatal("precondition: test store should hold a DEK before Close")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Store-level observable: the DEK is gone after Close. The byte-level
	// zeroing of the backing array is unit-tested against CloseSecrets in
	// engine/store/internal/crypto (state_test.go).
	if StoreHasDEK(s) {
		t.Error("DEK should be wiped after Close")
	}
}

// --- Edge cases: nil/empty fields ---

func TestClose_NilDEK_NoPanic(t *testing.T) {
	s := &store{
		state:  domain.StateLocked,
		crypto: crypto.New(nil, nil, nil, nil, nil),
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil dek: %v", err)
	}
}

func TestClose_EmptyDEK_NoPanic(t *testing.T) {
	s := &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, []byte{}, nil, nil, nil),
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on empty dek: %v", err)
	}
}

func TestClose_PartialCryptoState_NoPanic(t *testing.T) {
	s := &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, []byte{1, 2, 3}, nil, nil, nil),
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on partially populated crypto state: %v", err)
	}
}

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
			s.state = tc.from

			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !s.closed {
				t.Errorf("s.closed: want true after Close")
			}
		})
	}
}

// TestClose_OperationsReturnErrClosed verifies that after Close any
// operation gated on checkOperational returns os.ErrClosed, not
// ErrLocked — including Plain (unencrypted) stores, so a closed store
// never sends users hunting for a passphrase that does not exist.
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
			s.state = tc.from

			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			err := s.checkOperational()
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

	s := &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, append([]byte(nil), dek...), nil, resolver, nil),
	}

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
	s := &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, []byte{1, 2, 3}, nil, resolver, nil),
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if resolver.closed.Load() {
		t.Errorf("custom resolver.close() was called — host-owned resolvers must not be touched")
	}
}

func TestClose_NilKeyResolver_NoPanic(t *testing.T) {
	s := &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, []byte{1, 2, 3}, nil, nil, nil),
	}
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

	s := &store{
		state:  domain.StateUnlocked,
		crypto: crypto.New(nil, append([]byte(nil), dek...), nil, resolver, nil),
	}

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

	if !s.closed {
		t.Errorf("s.closed: want true after concurrent Closes")
	}
	if StoreHasDEK(s) {
		t.Error("DEK should be wiped after concurrent Closes")
	}
}

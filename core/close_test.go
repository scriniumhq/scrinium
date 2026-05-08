package core

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rkurbatov/scrinium/domain"
)

// --- Test scaffolding ---

// fakeKeyResolver is a custom KeyResolver: it MUST NOT be touched
// by Close. The test uses it to verify that Close does not call
// methods on host-supplied resolvers.
type fakeKeyResolver struct {
	closed atomic.Bool
}

func (r *fakeKeyResolver) GetKeys(string) ([][]byte, error) { return nil, nil }
func (r *fakeKeyResolver) DefaultKeyID() string             { return "" }
func (r *fakeKeyResolver) close()                           { r.closed.Store(true) }

// newTestStore builds a minimal *store sufficient to exercise
// Close. It does not stand up the Driver, Index, descriptor, or
// publisher — Close does not touch any of them.
func newTestStore() *store {
	return &store{
		state:           domain.StateUnlocked,
		dek:             []byte{1, 2, 3, 4, 5, 6, 7, 8},
		capabilityToken: []byte{0xAA, 0xBB, 0xCC, 0xDD},
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
	original := append([]byte(nil), s.dek...) // capture for length check
	dekRef := s.dek                           // grab the slice header — observe its bytes after Close

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if s.dek != nil {
		t.Errorf("s.dek: want nil after Close, got %v", s.dek)
	}
	if !allZero(dekRef) {
		t.Errorf("dek bytes should be zeroed, got %v (was %v)", dekRef, original)
	}
}

func TestClose_WipesCapabilityToken(t *testing.T) {
	s := newTestStore()
	original := append([]byte(nil), s.capabilityToken...)
	tokRef := s.capabilityToken

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if s.capabilityToken != nil {
		t.Errorf("s.capabilityToken: want nil after Close, got %v", s.capabilityToken)
	}
	if !allZero(tokRef) {
		t.Errorf("capability token bytes should be zeroed, got %v (was %v)", tokRef, original)
	}
}

// --- Edge cases: nil/empty fields ---

func TestClose_NilDEK_NoPanic(t *testing.T) {
	s := &store{
		state: domain.StateLocked,
		dek:   nil,
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil dek: %v", err)
	}
}

func TestClose_EmptyDEK_NoPanic(t *testing.T) {
	s := &store{
		state: domain.StateUnlocked,
		dek:   []byte{},
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on empty dek: %v", err)
	}
}

func TestClose_NoCapabilityToken_NoPanic(t *testing.T) {
	s := &store{
		state:           domain.StateUnlocked,
		dek:             []byte{1, 2, 3},
		capabilityToken: nil,
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil token: %v", err)
	}
}

// --- State transition ---

func TestClose_TransitionsToLocked(t *testing.T) {
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
			if got := s.State(); got != domain.StateLocked {
				t.Errorf("state after Close: got %v, want Locked", got)
			}
			if !s.closed {
				t.Errorf("s.closed: want true after Close")
			}
		})
	}
}

// --- KeyResolver handling ---

func TestClose_DefaultStaticKeyResolver_GetsClosed(t *testing.T) {
	dek := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	resolver := NewStaticKeyResolver(dek).(*staticKeyResolver)

	s := &store{
		state:       domain.StateUnlocked,
		dek:         append([]byte(nil), dek...),
		keyResolver: resolver,
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, GetKeys returns (nil, nil) — the codec turns
	// that into ErrKeyNotFound.
	keys, err := resolver.GetKeys("any")
	if err != nil {
		t.Errorf("GetKeys after close: unexpected err: %v", err)
	}
	if keys != nil {
		t.Errorf("GetKeys after close: want nil keys, got %v", keys)
	}
	// The internal dek slice is wiped and nil-ed.
	if resolver.dek != nil {
		t.Errorf("resolver.dek after close: want nil, got %v", resolver.dek)
	}
}

func TestClose_CustomKeyResolver_NotTouched(t *testing.T) {
	resolver := &fakeKeyResolver{}
	s := &store{
		state:       domain.StateUnlocked,
		dek:         []byte{1, 2, 3},
		keyResolver: resolver,
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
		state:       domain.StateUnlocked,
		dek:         []byte{1, 2, 3},
		keyResolver: nil,
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
	resolver := NewStaticKeyResolver(dek).(*staticKeyResolver)

	s := &store{
		state:       domain.StateUnlocked,
		dek:         append([]byte(nil), dek...),
		keyResolver: resolver,
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
	if s.dek != nil {
		t.Errorf("s.dek: want nil after concurrent Closes, got %v", s.dek)
	}
}

// --- Helpers ---

func allZero(b []byte) bool {
	zero := make([]byte, len(b))
	return bytes.Equal(b, zero)
}

package store_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"scrinium.dev/testutil/storefx"
)

// --- Integration: Close on a real Store ---
//
// These tests stand up a Store via storefx (full Driver + Index +
// crypto stack) and exercise Close end-to-end. They complement
// the unit-level tests in store_close_test.go (same package, tests the
// internal *store struct fields directly).
//
// What "closed" means: Close is a terminal condition surfaced as
// os.ErrClosed by every operation gated on checkOperational. It
// is distinct from StateLocked, which is reserved for an encrypted
// store awaiting Unlock. Conflating the two confused Plain-store
// users into hunting for a passphrase that did not exist; we now
// keep them separate.

// TestClose_PlainStore_BlocksWithErrClosed verifies Close on a
// Plain Store completes without error, and that subsequent
// operations refuse with os.ErrClosed (not ErrLocked — there is
// no passphrase to acquire).
func TestClose_PlainStore_BlocksWithErrClosed(t *testing.T) {
	s, _ := storefx.InitPlain(t)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Capacity is the canonical lightweight read; it goes through
	// checkOperational and is the cleanest probe for "is the store
	// reachable?".
	_, err := s.Capacity(context.Background())
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("Capacity after Close: got %v, want os.ErrClosed", err)
	}
}

// TestClose_EncryptedUnlocked_BlocksWithErrClosed verifies that
// closing an Unlocked encrypted Store wipes the DEK and surfaces
// os.ErrClosed, NOT ErrLocked. ErrLocked would imply a re-Unlock
// is possible — but Close is terminal, the Store is dead.
func TestClose_EncryptedUnlocked_BlocksWithErrClosed(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "hunter2")

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := s.Capacity(context.Background())
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("Capacity after Close: got %v, want os.ErrClosed", err)
	}
}

// TestClose_AfterClose_OperationsRefuseGracefully verifies that
// after Close, encryption-dependent operations refuse with
// os.ErrClosed rather than panicking on wiped state.
// ExportRecoveryKit is a useful probe: it touches secret material
// that Close has wiped, so any missing-Closed-gate would show up
// either as a panic or as success returning a zero/garbage kit.
func TestClose_AfterClose_OperationsRefuseGracefully(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "hunter2")

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ExportRecoveryKit panicked after Close: %v", r)
		}
	}()
	_, err := s.ExportRecoveryKit(context.Background())
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("ExportRecoveryKit after Close: got %v, want os.ErrClosed", err)
	}
}

// TestClose_Idempotent_Integration repeats Close on a real
// Store. Should be a no-op without errors.
func TestClose_Idempotent_Integration(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "pw")

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

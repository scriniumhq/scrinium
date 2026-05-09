package core_test

import (
	"context"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/internal/testutil/storefx"
)

// --- Integration: Close on a real Store ---
//
// These tests stand up a Store via storefx (full Driver + Index +
// crypto stack) and exercise Close end-to-end. They complement
// the unit-level tests in close_test.go (same package, tests the
// internal *store struct fields directly).

// TestClose_PlainStore_NoOpButTransitions verifies Close on a
// Plain Store completes without error and leaves State=Locked.
func TestClose_PlainStore_NoOpButTransitions(t *testing.T) {
	s, _ := storefx.InitPlain(t)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.State() != domain.StateLocked {
		t.Errorf("Plain Store after Close: state %v, want Locked", s.State())
	}
}

// TestClose_EncryptedUnlocked_ReturnsToLocked verifies Close on
// an Unlocked encrypted Store wipes the DEK (state-observable
// via subsequent operation refusal) and transitions to Locked.
func TestClose_EncryptedUnlocked_ReturnsToLocked(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "hunter2")

	// Sanity: starts Unlocked.
	if s.State() != domain.StateUnlocked {
		t.Fatalf("pre-Close: state %v, want Unlocked", s.State())
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if s.State() != domain.StateLocked {
		t.Errorf("post-Close: state %v, want Locked", s.State())
	}
}

// TestClose_AfterClose_OperationsRefuseGracefully verifies that
// after Close, encryption-dependent operations behave as on a
// Locked Store rather than panicking. We don't pin down the
// exact error (implementation-defined per AdminStore.Close
// doc), only that it returns an error and does not panic.
func TestClose_AfterClose_OperationsRefuseGracefully(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "hunter2")

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// ExportRecoveryKit on a Locked Store refuses; after Close
	// (state == Locked) the same refusal is the expected
	// behaviour.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ExportRecoveryKit panicked after Close: %v", r)
		}
	}()
	if _, err := s.ExportRecoveryKit(context.Background()); err == nil {
		t.Errorf("ExportRecoveryKit after Close: want error, got nil")
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

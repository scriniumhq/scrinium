package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
)

// putWithSession is a one-liner Put helper for rollback tests.
// Returns the resulting ArtifactID.
func putWithSession(t *testing.T, s store.Store, sid domain.SessionID, ns, payload string) domain.ArtifactID {
	t.Helper()
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: strings.NewReader(payload)},
		domain.WithNamespace(ns), domain.WithSession(sid))
	if err != nil {
		t.Fatalf("Put(sid=%q): %v", sid, err)
	}
	return id
}

// putWithRetention puts an artifact with the given session and an
// active retention window.
func putWithRetention(t *testing.T, s store.Store, sid domain.SessionID, ns, payload string, until time.Time) domain.ArtifactID {
	t.Helper()
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: strings.NewReader(payload)},
		domain.WithSession(sid), domain.WithNamespace(ns), domain.WithRetention(until))
	if err != nil {
		t.Fatalf("Put(sid=%q, retention): %v", sid, err)
	}
	return id
}

// walkCount returns the number of user-visible manifests.
func walkCount(t *testing.T, s store.Store) int {
	t.Helper()
	n := 0
	if err := s.Walk(context.Background(), "*", func(domain.Manifest) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return n
}

// --- Empty session ID guard ---

func TestRollbackSession_EmptyIDRejected(t *testing.T) {
	s := storefx.Init(t)
	err := s.RollbackSession(context.Background(), "")
	if !errors.Is(err, errs.ErrEmptySessionID) {
		t.Fatalf("expected errs.ErrEmptySessionID, got %v", err)
	}
}

// --- No-op on missing session ---

func TestRollbackSession_UnknownSessionIsNoOp(t *testing.T) {
	s := storefx.Init(t)
	if err := s.RollbackSession(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected nil for unknown session, got %v", err)
	}
}

// --- Single artifact rollback ---

func TestRollbackSession_SingleArtifactDeleted(t *testing.T) {
	s := storefx.Init(t)
	id := putWithSession(t, s, "imp-1", "users", "alpha")

	if err := s.RollbackSession(context.Background(), "imp-1"); err != nil {
		t.Fatalf("RollbackSession: %v", err)
	}

	if _, err := s.Get(context.Background(), id); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("artifact should be gone, got Get err = %v", err)
	}
	if got := walkCount(t, s); got != 0 {
		t.Errorf("Walk count: got %d, want 0", got)
	}
}

// --- Multiple artifacts in a session ---

func TestRollbackSession_AllArtifactsInSessionDeleted(t *testing.T) {
	s := storefx.Init(t)
	for i, payload := range []string{"a", "b", "c", "d"} {
		_ = putWithSession(t, s, "imp-2", "users", payload)
		_ = i
	}

	if err := s.RollbackSession(context.Background(), "imp-2"); err != nil {
		t.Fatalf("RollbackSession: %v", err)
	}
	if got := walkCount(t, s); got != 0 {
		t.Errorf("Walk count after rollback: got %d, want 0", got)
	}
}

// --- Mixed sessions: only the requested one is touched ---

func TestRollbackSession_OtherSessionsUntouched(t *testing.T) {
	s := storefx.Init(t)
	target := putWithSession(t, s, "imp-3", "users", "to-roll-back")
	_ = target
	keep1 := putWithSession(t, s, "imp-other", "users", "keep-1")
	keep2 := putWithSession(t, s, "", "users", "keep-no-session")

	if err := s.RollbackSession(context.Background(), "imp-3"); err != nil {
		t.Fatalf("RollbackSession: %v", err)
	}

	for _, kept := range []domain.ArtifactID{keep1, keep2} {
		if _, err := s.Get(context.Background(), kept); err != nil {
			t.Errorf("artifact %q should still exist, got Get err = %v", kept, err)
		}
	}
	if got := walkCount(t, s); got != 2 {
		t.Errorf("Walk count: got %d, want 2", got)
	}
}

// --- Atomic retention: if ANY artifact has active retention, nothing is deleted ---

func TestRollbackSession_RetentionBlocksWholeSession(t *testing.T) {
	s := storefx.Init(t)
	a := putWithSession(t, s, "imp-4", "users", "free-1")
	b := putWithRetention(t, s, "imp-4", "users", "protected", time.Now().Add(time.Hour))
	c := putWithSession(t, s, "imp-4", "users", "free-2")

	err := s.RollbackSession(context.Background(), "imp-4")
	if !errors.Is(err, errs.ErrRetentionNotExpired) {
		t.Fatalf("expected errs.ErrRetentionNotExpired, got %v", err)
	}

	// Atomicity: NONE of the three should be gone.
	for _, id := range []domain.ArtifactID{a, b, c} {
		if _, err := s.Get(context.Background(), id); err != nil {
			t.Errorf("artifact %q must survive blocked rollback, got %v", id, err)
		}
	}
	if got := walkCount(t, s); got != 3 {
		t.Errorf("Walk count: got %d, want 3 (atomic refusal)", got)
	}
}

// --- Retention does NOT block once expired ---

func TestRollbackSession_ExpiredRetentionAllowsRollback(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithRetention(t, s, "imp-5", "users", "was-protected", time.Now().Add(-time.Hour))
	_ = putWithSession(t, s, "imp-5", "users", "free")

	if err := s.RollbackSession(context.Background(), "imp-5"); err != nil {
		t.Fatalf("RollbackSession: %v", err)
	}
	if got := walkCount(t, s); got != 0 {
		t.Errorf("Walk count: got %d, want 0", got)
	}
}

// --- DeletionPolicy: NoDelete refuses the whole call ---

func TestRollbackSession_NoDeletePolicyRefusesAtomically(t *testing.T) {
	cfg := domain.StoreConfig{DeletionPolicy: domain.DeletionPolicyNoDelete}
	s := storefx.Init(t, store.WithConfig(cfg))

	a := putWithSession(t, s, "imp-6", "users", "x")
	b := putWithSession(t, s, "imp-6", "users", "y")

	err := s.RollbackSession(context.Background(), "imp-6")
	if !errors.Is(err, errs.ErrDeletionForbidden) {
		t.Fatalf("expected errs.ErrDeletionForbidden, got %v", err)
	}

	// Both artifacts must still be there.
	for _, id := range []domain.ArtifactID{a, b} {
		if _, err := s.Get(context.Background(), id); err != nil {
			t.Errorf("artifact %q must survive blocked rollback, got %v", id, err)
		}
	}
}

// --- Maintenance modes ---

func TestRollbackSession_BlockedInReadOnly(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithSession(t, s, "imp-7", "users", "z")
	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
		t.Fatalf("SetMaintenanceMode: %v", err)
	}
	err := s.RollbackSession(context.Background(), "imp-7")
	if !errors.Is(err, errs.ErrStoreReadOnly) {
		t.Fatalf("expected errs.ErrStoreReadOnly, got %v", err)
	}
}

func TestRollbackSession_BlockedInOffline(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithSession(t, s, "imp-8", "users", "z")
	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeOffline); err != nil {
		t.Fatalf("SetMaintenanceMode: %v", err)
	}
	err := s.RollbackSession(context.Background(), "imp-8")
	if !errors.Is(err, errs.ErrStoreOffline) {
		t.Fatalf("expected errs.ErrStoreOffline, got %v", err)
	}
}

// --- Cancellation ---

func TestRollbackSession_CtxCancelled(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithSession(t, s, "imp-9", "users", "z")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.RollbackSession(ctx, "imp-9")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- Idempotency on success ---

func TestRollbackSession_IdempotentOnSuccess(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithSession(t, s, "imp-10", "users", "x")
	_ = putWithSession(t, s, "imp-10", "users", "y")

	if err := s.RollbackSession(context.Background(), "imp-10"); err != nil {
		t.Fatalf("first RollbackSession: %v", err)
	}
	if err := s.RollbackSession(context.Background(), "imp-10"); err != nil {
		t.Fatalf("second RollbackSession (idempotent): %v", err)
	}
}

// --- Idempotency under partial-progress simulation ---
//
// We simulate the "crash mid-rollback" recovery by calling
// store.Delete on one artifact in the session, then invoking
// RollbackSession. The remaining artifacts must be cleaned up,
// and the missing one must not re-surface as an error.
func TestRollbackSession_ResumesAfterPartialDelete(t *testing.T) {
	s := storefx.Init(t)
	a := putWithSession(t, s, "imp-11", "users", "x")
	b := putWithSession(t, s, "imp-11", "users", "y")
	c := putWithSession(t, s, "imp-11", "users", "z")

	// Pretend rollback was interrupted after deleting one.
	if err := s.Delete(context.Background(), b); err != nil {
		t.Fatalf("setup Delete: %v", err)
	}

	if err := s.RollbackSession(context.Background(), "imp-11"); err != nil {
		t.Fatalf("RollbackSession (resume): %v", err)
	}

	for _, id := range []domain.ArtifactID{a, b, c} {
		if _, err := s.Get(context.Background(), id); !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Errorf("artifact %q should be gone, got %v", id, err)
		}
	}
	if got := walkCount(t, s); got != 0 {
		t.Errorf("Walk count: got %d, want 0", got)
	}
}

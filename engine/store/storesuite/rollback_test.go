// RollbackSession: delete every artifact written under a session id.
// Guards (empty id, read-only, offline, cancelled ctx) and atomic refusals
// (active retention, no-delete policy — nothing is deleted) are tables; the
// successful-rollback shapes (single / multiple / expired-retention),
// selective scope, unknown-session no-op, idempotency, and crash-resume are
// focused tests.

package storesuite

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
	"scrinium.dev/testutil/storekit"
)

// putWithSession puts an artifact under a session and returns its id.
func putWithSession(t *testing.T, s store.Store, sid domain.SessionID, payload string) domain.ArtifactID {
	t.Helper()
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: strings.NewReader(payload)},
		domain.WithSession(sid))
	if err != nil {
		t.Fatalf("Put(sid=%q): %v", sid, err)
	}
	return id
}

// putWithRetention puts an artifact under a session with an active
// retention window and returns its id.
func putWithRetention(t *testing.T, s store.Store, sid domain.SessionID, payload string, until time.Time) domain.ArtifactID {
	t.Helper()
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: strings.NewReader(payload)},
		domain.WithSession(sid), domain.WithRetention(until))
	if err != nil {
		t.Fatalf("Put(sid=%q, retention): %v", sid, err)
	}
	return id
}

// TestRollbackSession_DeletesSession: rolling back a session removes every
// artifact written under it (each Get → ErrArtifactNotFound, Walk empty) —
// for a single artifact, many artifacts, and a session whose retention has
// already expired.
func TestRollbackSession_DeletesSession(t *testing.T) {
	const sid domain.SessionID = "imp"
	cases := []struct {
		name  string
		setup func(t *testing.T, s store.Store) []domain.ArtifactID
	}{
		{"single artifact", func(t *testing.T, s store.Store) []domain.ArtifactID {
			return []domain.ArtifactID{putWithSession(t, s, sid, "alpha")}
		}},
		{"multiple artifacts", func(t *testing.T, s store.Store) []domain.ArtifactID {
			var ids []domain.ArtifactID
			for _, p := range []string{"a", "b", "c", "d"} {
				ids = append(ids, putWithSession(t, s, sid, p))
			}
			return ids
		}},
		{"expired retention does not block", func(t *testing.T, s store.Store) []domain.ArtifactID {
			return []domain.ArtifactID{
				putWithRetention(t, s, sid, "was-protected", time.Now().Add(-time.Hour)),
				putWithSession(t, s, sid, "free"),
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := storefx.Init(t)
			ids := tc.setup(t, s)
			if err := s.RollbackSession(context.Background(), sid); err != nil {
				t.Fatalf("RollbackSession: %v", err)
			}
			for _, id := range ids {
				if _, err := s.Get(context.Background(), id); !errors.Is(err, errs.ErrArtifactNotFound) {
					t.Errorf("artifact %q should be gone, got %v", id, err)
				}
			}
			if got := storekit.WalkCount(t, s); got != 0 {
				t.Errorf("Walk count: got %d, want 0", got)
			}
		})
	}
}

// TestRollbackSession_OtherSessionsUntouched: only the requested session is
// rolled back; other sessions (and the default no-session bucket) survive.
func TestRollbackSession_OtherSessionsUntouched(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithSession(t, s, "imp-3", "to-roll-back")
	keep1 := putWithSession(t, s, "imp-other", "keep-1")
	keep2 := putWithSession(t, s, "", "keep-no-session")

	if err := s.RollbackSession(context.Background(), "imp-3"); err != nil {
		t.Fatalf("RollbackSession: %v", err)
	}
	for _, kept := range []domain.ArtifactID{keep1, keep2} {
		if _, err := s.Get(context.Background(), kept); err != nil {
			t.Errorf("artifact %q should still exist, got %v", kept, err)
		}
	}
	if got := storekit.WalkCount(t, s); got != 2 {
		t.Errorf("Walk count: got %d, want 2", got)
	}
}

// TestRollbackSession_UnknownSessionIsNoOp: rolling back a session that
// never existed succeeds with no error.
func TestRollbackSession_UnknownSessionIsNoOp(t *testing.T) {
	s := storefx.Init(t)
	if err := s.RollbackSession(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected nil for unknown session, got %v", err)
	}
}

// TestRollbackSession_AtomicRefusal: a rollback that hits an active
// retention window (ErrRetentionNotExpired) or a NoDelete policy
// (ErrDeletionForbidden) deletes nothing — every artifact survives.
func TestRollbackSession_AtomicRefusal(t *testing.T) {
	cases := []struct {
		name    string
		init    func(t *testing.T) store.Store
		setup   func(t *testing.T, s store.Store) []domain.ArtifactID
		want    error
		wantCnt int
	}{
		{"active retention blocks whole session",
			func(t *testing.T) store.Store { return storefx.Init(t) },
			func(t *testing.T, s store.Store) []domain.ArtifactID {
				return []domain.ArtifactID{
					putWithSession(t, s, "imp", "free-1"),
					putWithRetention(t, s, "imp", "protected", time.Now().Add(time.Hour)),
					putWithSession(t, s, "imp", "free-2"),
				}
			}, errs.ErrRetentionNotExpired, 3},
		{"no-delete policy refuses",
			func(t *testing.T) store.Store {
				return storefx.Init(t, store.WithConfig(domain.StoreConfig{DeletionPolicy: domain.DeletionPolicyNoDelete}))
			},
			func(t *testing.T, s store.Store) []domain.ArtifactID {
				return []domain.ArtifactID{
					putWithSession(t, s, "imp", "x"),
					putWithSession(t, s, "imp", "y"),
				}
			}, errs.ErrDeletionForbidden, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.init(t)
			ids := tc.setup(t, s)
			if err := s.RollbackSession(context.Background(), "imp"); !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
			}
			for _, id := range ids {
				if _, err := s.Get(context.Background(), id); err != nil {
					t.Errorf("%s: artifact %q must survive blocked rollback, got %v", tc.name, id, err)
				}
			}
			if got := storekit.WalkCount(t, s); got != tc.wantCnt {
				t.Errorf("%s: Walk count: got %d, want %d (atomic refusal)", tc.name, got, tc.wantCnt)
			}
		})
	}
}

// TestRollbackSession_Guards: empty id, read-only, offline, and a cancelled
// context each refuse with their sentinel.
func TestRollbackSession_Guards(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
		want error
	}{
		{"empty session id", func(t *testing.T) error {
			s := storefx.Init(t)
			return s.RollbackSession(context.Background(), "")
		}, errs.ErrEmptySessionID},
		{"read-only mode", func(t *testing.T) error {
			s := storefx.Init(t)
			_ = putWithSession(t, s, "imp", "z")
			if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
				t.Fatalf("SetMaintenanceMode: %v", err)
			}
			return s.RollbackSession(context.Background(), "imp")
		}, errs.ErrStoreReadOnly},
		{"offline mode", func(t *testing.T) error {
			s := storefx.Init(t)
			_ = putWithSession(t, s, "imp", "z")
			if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeOffline); err != nil {
				t.Fatalf("SetMaintenanceMode: %v", err)
			}
			return s.RollbackSession(context.Background(), "imp")
		}, errs.ErrStoreOffline},
		{"cancelled context", func(t *testing.T) error {
			s := storefx.Init(t)
			_ = putWithSession(t, s, "imp", "z")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return s.RollbackSession(ctx, "imp")
		}, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t); !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestRollbackSession_IdempotentOnSuccess: a second rollback of an
// already-rolled-back session is a no-op.
func TestRollbackSession_IdempotentOnSuccess(t *testing.T) {
	s := storefx.Init(t)
	_ = putWithSession(t, s, "imp-10", "x")
	_ = putWithSession(t, s, "imp-10", "y")

	if err := s.RollbackSession(context.Background(), "imp-10"); err != nil {
		t.Fatalf("first RollbackSession: %v", err)
	}
	if err := s.RollbackSession(context.Background(), "imp-10"); err != nil {
		t.Fatalf("second RollbackSession (idempotent): %v", err)
	}
}

// TestRollbackSession_ResumesAfterPartialDelete: simulating a crash
// mid-rollback (one artifact already individually deleted), a fresh
// RollbackSession cleans up the rest and does not error on the missing one.
func TestRollbackSession_ResumesAfterPartialDelete(t *testing.T) {
	s := storefx.Init(t)
	a := putWithSession(t, s, "imp-11", "x")
	b := putWithSession(t, s, "imp-11", "y")
	c := putWithSession(t, s, "imp-11", "z")

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
	if got := storekit.WalkCount(t, s); got != 0 {
		t.Errorf("Walk count: got %d, want 0", got)
	}
}

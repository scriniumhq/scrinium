// Cross-operation tables. State guards, context cancellation, retention
// and not-found/ID-validation are the same shape across Put/Get/Delete,
// so they live here as tables rather than as one function per
// (operation, condition) pair across three files. Per TESTING.md these
// are category 6 (enumerable facts).
//
// Note: the operation runners are named putOp/getOp/deleteOp to avoid
// clashing with the opPut/opGet/opDelete opKind constants in
// store_model_test.go (same package).

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
	"scrinium.dev/internal/testutil/storefx"
)

// storeOp is one store operation, parameterised so the guard and ctx
// tables can drive Put/Get/Delete uniformly. id is the seed artifact
// for operations that need an existing one (ignored by Put).
type storeOp struct {
	name string
	run  func(ctx context.Context, s store.Store, id domain.ArtifactID) error
}

var (
	putOp = storeOp{"Put", func(ctx context.Context, s store.Store, _ domain.ArtifactID) error {
		_, err := s.Put(ctx, payload("x"), store.WithNamespace("u"))
		return err
	}}
	getOp = storeOp{"Get", func(ctx context.Context, s store.Store, id domain.ArtifactID) error {
		rh, err := s.Get(ctx, id)
		if err == nil {
			rh.Close()
		}
		return err
	}}
	deleteOp = storeOp{"Delete", func(ctx context.Context, s store.Store, id domain.ArtifactID) error {
		return s.Delete(ctx, id)
	}}
)

// mustPut seeds a fresh artifact and returns its id.
func mustPut(t *testing.T, s store.Store) domain.ArtifactID {
	t.Helper()
	id, err := s.Put(context.Background(), payload("seed"), store.WithNamespace("seed"))
	if err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	return id
}

// TestStore_StateGuards: which operations are blocked in which
// maintenance mode. Get is read-only and stays allowed under ReadOnly;
// everything is blocked Offline; mutations are blocked under ReadOnly.
// The NoDelete deletion policy is a separate (config, not mode) gate.
func TestStore_StateGuards(t *testing.T) {
	cases := []struct {
		label string
		op    storeOp
		mode  domain.MaintenanceMode
		want  error // nil = operation allowed in this mode
	}{
		{"put/readonly", putOp, domain.MaintenanceModeReadOnly, errs.ErrStoreReadOnly},
		{"put/offline", putOp, domain.MaintenanceModeOffline, errs.ErrStoreOffline},
		{"get/readonly", getOp, domain.MaintenanceModeReadOnly, nil},
		{"get/offline", getOp, domain.MaintenanceModeOffline, errs.ErrStoreOffline},
		{"delete/readonly", deleteOp, domain.MaintenanceModeReadOnly, errs.ErrStoreReadOnly},
		{"delete/offline", deleteOp, domain.MaintenanceModeOffline, errs.ErrStoreOffline},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			s, _ := storefx.InitWithRoot(t)
			id := mustPut(t, s)
			if err := s.SetMaintenanceMode(context.Background(), tc.mode); err != nil {
				t.Fatalf("SetMaintenanceMode: %v", err)
			}
			err := tc.op.run(context.Background(), s, id)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("%s: got %v, want allowed", tc.label, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.label, err, tc.want)
			}
		})
	}

	t.Run("delete/no-delete-policy", func(t *testing.T) {
		cfg := domain.StoreConfig{DeletionPolicy: domain.DeletionPolicyNoDelete}
		s, _ := storefx.InitWithRoot(t, store.WithConfig(cfg))
		id := mustPut(t, s)
		if err := s.Delete(context.Background(), id); !errors.Is(err, errs.ErrDeletionForbidden) {
			t.Fatalf("got %v, want errs.ErrDeletionForbidden", err)
		}
	})
}

// TestStore_CtxCancelled: a cancelled context fails every operation
// with context.Canceled before any work is committed.
func TestStore_CtxCancelled(t *testing.T) {
	for _, op := range []storeOp{putOp, getOp, deleteOp} {
		op := op
		t.Run(op.name, func(t *testing.T) {
			s, _ := storefx.InitWithRoot(t)
			id := mustPut(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := op.run(ctx, s, id); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: got %v, want context.Canceled", op.name, err)
			}
		})
	}
}

// TestStore_NotFoundAndIDValidation: Get/Delete of a missing or empty
// ID return ErrArtifactNotFound, and a second Delete of a just-deleted
// artifact is NotFound (the natural CAS semantics).
func TestStore_NotFoundAndIDValidation(t *testing.T) {
	missing := domain.ArtifactID("sha256-" + strings.Repeat("0", 64))
	cases := []struct {
		name string
		run  func(ctx context.Context, s store.Store) error
	}{
		{"Get missing", func(ctx context.Context, s store.Store) error {
			_, e := s.Get(ctx, missing)
			return e
		}},
		{"Get empty id", func(ctx context.Context, s store.Store) error {
			_, e := s.Get(ctx, "")
			return e
		}},
		{"Delete missing", func(ctx context.Context, s store.Store) error {
			return s.Delete(ctx, missing)
		}},
		{"Delete empty id", func(ctx context.Context, s store.Store) error {
			return s.Delete(ctx, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := storefx.InitWithRoot(t)
			if err := tc.run(context.Background(), s); !errors.Is(err, errs.ErrArtifactNotFound) {
				t.Fatalf("%s: got %v, want errs.ErrArtifactNotFound", tc.name, err)
			}
		})
	}

	t.Run("Delete twice is NotFound", func(t *testing.T) {
		s, _ := storefx.InitWithRoot(t)
		id := mustPut(t, s)
		if err := s.Delete(context.Background(), id); err != nil {
			t.Fatalf("first Delete: %v", err)
		}
		if err := s.Delete(context.Background(), id); !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Errorf("second Delete: got %v, want errs.ErrArtifactNotFound", err)
		}
	})
}

// TestStore_Retention: RetentionUntil is persisted and blocks Delete
// until it expires, and retention is checked BEFORE the deletion
// policy (a NoDelete store still reports ErrRetentionNotExpired, not
// ErrDeletionForbidden, when both apply).
func TestStore_Retention(t *testing.T) {
	future := func() time.Time { return time.Now().Add(time.Hour).UTC().Truncate(time.Second) }

	t.Run("Put preserves RetentionUntil", func(t *testing.T) {
		s, _ := storefx.InitWithRoot(t)
		when := future()
		id, err := s.Put(context.Background(), payload("retained"),
			store.WithNamespace("vault"), store.WithRetention(when))
		if err != nil {
			t.Fatal(err)
		}
		var seen domain.Manifest
		if err := s.Walk(context.Background(), "vault", func(m domain.Manifest) error {
			if m.ArtifactID == id {
				seen = m
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !seen.RetentionUntil.Equal(when) {
			t.Errorf("RetentionUntil: got %v, want %v", seen.RetentionUntil, when)
		}
	})

	t.Run("Delete blocked by active retention", func(t *testing.T) {
		s, _ := storefx.InitWithRoot(t)
		id, err := s.Put(context.Background(), payload("retained"),
			store.WithNamespace("v"), store.WithRetention(future()))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(context.Background(), id); !errors.Is(err, errs.ErrRetentionNotExpired) {
			t.Fatalf("got %v, want errs.ErrRetentionNotExpired", err)
		}
	})

	t.Run("Delete allowed after expiry", func(t *testing.T) {
		s, _ := storefx.InitWithRoot(t)
		past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
		id, err := s.Put(context.Background(), payload("expired"),
			store.WithNamespace("v"), store.WithRetention(past))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(context.Background(), id); err != nil {
			t.Errorf("Delete after expiry: %v", err)
		}
	})

	t.Run("retention beats no-delete policy", func(t *testing.T) {
		cfg := domain.StoreConfig{DeletionPolicy: domain.DeletionPolicyNoDelete}
		s, _ := storefx.InitWithRoot(t, store.WithConfig(cfg))
		id, err := s.Put(context.Background(), payload("both"),
			store.WithRetention(future()))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(context.Background(), id); !errors.Is(err, errs.ErrRetentionNotExpired) {
			t.Fatalf("retention must beat policy; got %v", err)
		}
	})
}

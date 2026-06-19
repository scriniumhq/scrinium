package storesuite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
)

// Thin surface tests for the Store facade: the small, mechanical guarantees of
// the public read/admin methods that don't warrant a file of their own —
// State/Capabilities passthrough, MaintenanceMode acceptance/rejection,
// Capacity counting (and its driver-sourced blob count), and
// context-cancellation on the read paths.
//
// Mutating-path guards (Put/Get/Delete under ReadOnly/Offline, not-found, ID
// validation, retention) live in guards_test.go; this file covers the
// remaining read/admin surface only.

// --- State / Capabilities ---

func TestStore_State_StartsUnlocked(t *testing.T) {
	s := storefx.Init(t)
	if s.State() != domain.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), domain.StateUnlocked)
	}
}

func TestStore_Capabilities_DriverPassthrough(t *testing.T) {
	s := storefx.Init(t)
	if caps := s.Capabilities(); caps == 0 {
		t.Error("expected non-zero capabilities from localfs driver")
	}
}

// --- SetMaintenanceMode ---

// TestStore_SetMaintenanceMode covers acceptance of every valid mode (cycling
// back to None) and rejection of an out-of-range value.
func TestStore_SetMaintenanceMode(t *testing.T) {
	ctx := context.Background()

	t.Run("valid modes accepted", func(t *testing.T) {
		s := storefx.Init(t)
		for _, mode := range []domain.MaintenanceMode{
			domain.MaintenanceModeNone,
			domain.MaintenanceModeReadOnly,
			domain.MaintenanceModeOffline,
			domain.MaintenanceModeNone, // back to normal
		} {
			if err := s.SetMaintenanceMode(ctx, mode); err != nil {
				t.Errorf("SetMaintenanceMode(%d): %v", mode, err)
			}
		}
	})

	t.Run("invalid mode rejected", func(t *testing.T) {
		s := storefx.Init(t)
		err := s.SetMaintenanceMode(ctx, domain.MaintenanceMode(99))
		if err == nil {
			t.Fatal("expected error on invalid mode")
		}
		if !strings.Contains(err.Error(), "invalid mode") {
			t.Errorf("error message: %v", err)
		}
	})
}

// TestStore_SetMaintenanceMode_OfflineBlocksCapacity verifies the
// priority-of-checks flow surfaces errs.ErrStoreOffline through Capacity, and
// that returning to None restores it. (The Put/Get/Delete equivalents are in
// guards_test.go.)
func TestStore_SetMaintenanceMode_OfflineBlocksCapacity(t *testing.T) {
	s := storefx.Init(t)
	ctx := context.Background()

	if err := s.SetMaintenanceMode(ctx, domain.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capacity(ctx); !errors.Is(err, errs.ErrStoreOffline) {
		t.Fatalf("expected errs.ErrStoreOffline, got %v", err)
	}

	// Returning to None must restore Capacity.
	if err := s.SetMaintenanceMode(ctx, domain.MaintenanceModeNone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capacity(ctx); err != nil {
		t.Errorf("Capacity should work after None: %v", err)
	}
}

// --- Capacity ---

func TestStore_Capacity_FreshStoreIsEmpty(t *testing.T) {
	s := storefx.Init(t)
	info, err := s.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 0 {
		t.Errorf("ArtifactCount: got %d, want 0", info.ArtifactCount)
	}
	if info.BlobCount != 0 {
		t.Errorf("BlobCount: got %d, want 0", info.BlobCount)
	}
	// Byte sentinels: -1 means "unavailable" — see StorageInfo doc.
	if info.TotalBytes != -1 || info.AvailableBytes != -1 || info.UsedBytes != -1 {
		t.Errorf("expected -1 sentinels for byte fields, got %+v", info)
	}
}

// TestStore_Capacity_BlobCountReflectsDriver verifies BlobCount is sourced from
// the driver, not the index — so orphan blobs (files on disk with no matching
// index row) still show up, which is what makes Capacity useful for diagnosing
// recovery situations. ArtifactCount, in contrast, comes from the index and is
// unaffected by orphan manifest files.
func TestStore_Capacity_BlobCountReflectsDriver(t *testing.T) {
	s, root := storefx.InitWithRoot(t)

	// Drop orphan blob files directly via the filesystem — the Driver is wired
	// through s but not exported.
	for _, p := range []string{"blobs/aa/blob-1", "blobs/aa/blob-2", "blobs/bb/blob-3"} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	info, err := s.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.BlobCount != 3 {
		t.Errorf("BlobCount: got %d, want 3", info.BlobCount)
	}
	// ArtifactCount is index-sourced, so no user manifests exist
	// (system.config is filtered out by the "*" wildcard).
	if info.ArtifactCount != 0 {
		t.Errorf("ArtifactCount: got %d, want 0 (orphan manifests don't count)", info.ArtifactCount)
	}
}

// --- context cancellation (read paths) ---

// TestStore_CtxCancelled_ReadOps confirms the read-side methods honour a
// cancelled context. (The Put/Get/Delete equivalents live in guards_test.go's
// own cancellation table.)
func TestStore_CtxCancelled_ReadOps(t *testing.T) {
	cases := []struct {
		name string
		call func(context.Context, store.Store) error
	}{
		{"Capacity", func(ctx context.Context, s store.Store) error {
			_, err := s.Capacity(ctx)
			return err
		}},
		{"Walk", func(ctx context.Context, s store.Store) error {
			return s.Walk(ctx, func(domain.Manifest) error { return nil })
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := storefx.Init(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tc.call(ctx, s); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: expected context.Canceled, got %v", tc.name, err)
			}
		})
	}
}

// --- Walk ---

func TestStore_Walk_EmptyStore(t *testing.T) {
	s := storefx.Init(t)
	var seen int
	err := s.Walk(context.Background(), func(m domain.Manifest) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if seen != 0 {
		t.Errorf("walked %d manifests on empty Store", seen)
	}
}

package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
)

var newStore = storefx.Init

// --- State / Capabilities ---

func TestStore_State_StartsUnlocked(t *testing.T) {
	s := newStore(t)
	if s.State() != core.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), core.StateUnlocked)
	}
}

func TestStore_Capabilities_DriverPassthrough(t *testing.T) {
	s := newStore(t)
	caps := s.Capabilities()
	if caps == 0 {
		t.Error("expected non-zero capabilities from localfs driver")
	}
}

// --- SetMaintenanceMode ---

func TestStore_SetMaintenanceMode_AllValidValues(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, mode := range []core.MaintenanceMode{
		core.MaintenanceModeNone,
		core.MaintenanceModeReadOnly,
		core.MaintenanceModeOffline,
		core.MaintenanceModeNone, // back to normal
	} {
		if err := s.SetMaintenanceMode(ctx, mode); err != nil {
			t.Errorf("SetMaintenanceMode(%d): %v", mode, err)
		}
	}
}

func TestStore_SetMaintenanceMode_RejectsInvalid(t *testing.T) {
	s := newStore(t)
	err := s.SetMaintenanceMode(context.Background(), core.MaintenanceMode(99))
	if err == nil {
		t.Fatal("expected error on invalid mode")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("error message: %v", err)
	}
}

// TestStore_SetMaintenanceMode_OfflineBlocksReads verifies that the
// priority-of-checks flow surfaces errs.ErrStoreOffline through the
// public methods that consult it (Capacity is the M1.3 example;
// Put/Get/Delete arrive in M1.4).
func TestStore_SetMaintenanceMode_OfflineBlocksCapacity(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.SetMaintenanceMode(ctx, core.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	_, err := s.Capacity(ctx)
	if !errors.Is(err, errs.ErrStoreOffline) {
		t.Fatalf("expected errs.ErrStoreOffline, got %v", err)
	}

	// Returning to None must restore Capacity.
	if err := s.SetMaintenanceMode(ctx, core.MaintenanceModeNone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capacity(ctx); err != nil {
		t.Errorf("Capacity should work after None: %v", err)
	}
}

// --- Capacity ---

func TestStore_Capacity_FreshStoreIsEmpty(t *testing.T) {
	s := newStore(t)
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

// TestStore_Capacity_BlobCountReflectsDriver verifies that BlobCount
// is sourced from the driver, not the index — so orphan blobs (files
// on disk with no matching index row, e.g. between Driver.Put and
// IndexManifest in the Put pipeline, or pre-GC) still show up. This
// is what makes Capacity useful for diagnosing recovery situations.
//
// ArtifactCount, in contrast, comes from the index and is unaffected
// by orphan manifest files.
func TestStore_Capacity_BlobCountReflectsDriver(t *testing.T) {
	s, root := storefx.InitWithRoot(t)

	// Drop orphan blob files directly via the filesystem — Driver
	// is wired through s but newStore does not export it.
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

func TestStore_Capacity_CtxCancelled(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Capacity(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- Walk ---

func TestStore_Walk_EmptyStore(t *testing.T) {
	s := newStore(t)
	var seen int
	err := s.Walk(context.Background(), "*", func(m domain.Manifest) error {
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

func TestStore_Walk_RejectsSystemPrefix(t *testing.T) {
	s := newStore(t)
	err := s.Walk(context.Background(), "system.config", func(m domain.Manifest) error {
		t.Fatal("callback must not run for reserved namespace")
		return nil
	})
	if !errors.Is(err, errs.ErrReservedNamespace) {
		t.Fatalf("expected errs.ErrReservedNamespace, got %v", err)
	}
}

func TestStore_Walk_RejectsTooLongNamespace(t *testing.T) {
	s := newStore(t)
	long := strings.Repeat("a", 256)
	err := s.Walk(context.Background(), long, func(m domain.Manifest) error {
		return nil
	})
	if !errors.Is(err, errs.ErrNamespaceTooLong) {
		t.Fatalf("expected errs.ErrNamespaceTooLong, got %v", err)
	}
}

func TestStore_Walk_AcceptsEmptyAndWildcard(t *testing.T) {
	s := newStore(t)
	for _, ns := range []string{"", "*"} {
		err := s.Walk(context.Background(), ns, func(m domain.Manifest) error {
			return nil
		})
		if err != nil {
			t.Errorf("Walk(%q): %v", ns, err)
		}
	}
}

func TestStore_Walk_CtxCancelled(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Walk(ctx, "*", func(m domain.Manifest) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- WalkSystem ---

func TestStore_WalkSystem_AcceptsAllFourReserved(t *testing.T) {
	s := newStore(t)
	for _, ns := range []string{
		"system.transit",
		"system.manifests",
		"system.state",
		"system.config",
	} {
		err := s.WalkSystem(context.Background(), ns, func(m domain.Manifest) error {
			return nil
		})
		if err != nil {
			t.Errorf("WalkSystem(%q): %v", ns, err)
		}
	}
}

func TestStore_WalkSystem_RejectsNonReserved(t *testing.T) {
	s := newStore(t)
	for _, ns := range []string{
		"",                   // empty
		"*",                  // wildcard
		"users",              // user namespace
		"system.unknown",     // unknown system namespace
		"system.transit.foo", // sub-prefix, not exact
	} {
		err := s.WalkSystem(context.Background(), ns, func(m domain.Manifest) error {
			t.Fatalf("callback must not run for %q", ns)
			return nil
		})
		if !errors.Is(err, errs.ErrReservedNamespace) {
			t.Errorf("WalkSystem(%q): expected errs.ErrReservedNamespace, got %v", ns, err)
		}
	}
}

// --- Stub methods stay stubs ---

func TestStore_StubsM14_StillUnimplemented(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Methods that arrive with the data path in M1.4.
	if _, err := s.Put(ctx, domain.Artifact{}, core.PutOptions{}); err == nil {
		t.Error("Put should be a stub")
	}
	if _, err := s.Get(ctx, "x", core.GetOptions{}); err == nil {
		t.Error("Get should be a stub")
	}
	if err := s.Delete(ctx, "x"); err == nil {
		t.Error("Delete should be a stub")
	}
	if err := s.Verify(ctx, "x"); err == nil {
		t.Error("Verify should be a stub")
	}
	if err := s.RollbackSession(ctx, "sess"); err == nil {
		t.Error("RollbackSession should be a stub")
	}

	// AdminStore stubs (Unlock arrives with the crypto pipeline,
	// UpdateConfig with the config-pointer artifact wiring).
	if err := s.Unlock(ctx); err == nil {
		t.Error("Unlock should be a stub")
	}
	if err := s.RotateKEK(ctx); err == nil {
		t.Error("RotateKEK should be a stub")
	}
	if _, err := s.ExportRecoveryKit(ctx); err == nil {
		t.Error("ExportRecoveryKit should be a stub")
	}
	if err := s.UpdateConfig(ctx, domain.StoreConfig{}); err == nil {
		t.Error("UpdateConfig should be a stub")
	}
	if _, err := s.ConfigHistory(ctx); err == nil {
		t.Error("ConfigHistory should be a stub")
	}
}

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// TestRestoreDescriptorFromRecoveryKit_RoundTrip bootstraps an encrypted
// Store (which emits a Recovery Kit), deletes both descriptor replicas
// to simulate catastrophic loss, then restores them from the kit and
// asserts the identity and crypto material round-trip exactly.
func TestRestoreDescriptorFromRecoveryKit_RoundTrip(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)

	_, kit, err := store.InitStore(ctx, drv,
		store.WithHashRegistry(storefx.Hashes()),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithPassphrase(storefx.StaticPP("pw")),
	)
	if err != nil {
		t.Fatalf("InitStore (encrypted): %v", err)
	}
	if len(kit) == 0 {
		t.Fatal("InitStore returned an empty recovery kit for an encrypted store")
	}

	orig, err := descriptor.Read(ctx, drv)
	if err != nil {
		t.Fatalf("read original descriptor: %v", err)
	}

	// Simulate catastrophic descriptor loss: remove both replicas.
	root := drv.Root()
	for _, name := range []string{descriptor.Path, descriptor.BackupPath} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if _, err := descriptor.Read(ctx, drv); err == nil {
		t.Fatal("descriptor still readable after removing both replicas")
	}

	info, err := store.RestoreDescriptorFromRecoveryKit(ctx, drv, kit)
	if err != nil {
		t.Fatalf("RestoreDescriptorFromRecoveryKit: %v", err)
	}
	if !info.DescriptorWritten {
		t.Error("DescriptorWritten = false, want true")
	}
	if info.StoreID != orig.StoreID {
		t.Errorf("info.StoreID = %q, want %q", info.StoreID, orig.StoreID)
	}

	restored, err := descriptor.Read(ctx, drv)
	if err != nil {
		t.Fatalf("read restored descriptor (L0): %v", err)
	}
	if restored.StoreID != orig.StoreID {
		t.Errorf("StoreID = %q, want %q", restored.StoreID, orig.StoreID)
	}
	if !restored.DEKEncrypted {
		t.Error("restored descriptor not marked DEKEncrypted")
	}
	if !bytes.Equal(restored.DEK, orig.DEK) {
		t.Error("restored wrapped DEK differs from the original")
	}
	if restored.KDFParams == nil || orig.KDFParams == nil {
		t.Fatal("KDFParams missing on original or restored descriptor")
	}
	if restored.KDFParams.Algorithm != orig.KDFParams.Algorithm {
		t.Errorf("KDF algorithm = %q, want %q", restored.KDFParams.Algorithm, orig.KDFParams.Algorithm)
	}
	if !bytes.Equal(restored.KDFParams.Salt, orig.KDFParams.Salt) {
		t.Error("restored KDF salt differs from the original")
	}
	if restored.KDFParams.Time != orig.KDFParams.Time ||
		restored.KDFParams.Memory != orig.KDFParams.Memory ||
		restored.KDFParams.Threads != orig.KDFParams.Threads {
		t.Error("restored KDF cost parameters differ from the original")
	}

	// The L1 shadow replica was written too.
	rc, err := drv.Get(ctx, descriptor.BackupPath)
	if err != nil {
		t.Fatalf("L1 shadow descriptor not restored: %v", err)
	}
	_ = rc.Close()
}

// TestRestoreDescriptorFromRecoveryKit_Corrupted feeds non-kit bytes and
// expects the corrupted-kit sentinel.
func TestRestoreDescriptorFromRecoveryKit_Corrupted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, err := store.RestoreDescriptorFromRecoveryKit(context.Background(), drv, []byte("not a recovery kit"))
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("err = %v, want ErrRecoveryKitCorrupted", err)
	}
}

// TestRestoreDescriptorFromRecoveryKit_NilDriver guards the nil-driver
// programming error.
func TestRestoreDescriptorFromRecoveryKit_NilDriver(t *testing.T) {
	if _, err := store.RestoreDescriptorFromRecoveryKit(context.Background(), nil, []byte("x")); err == nil {
		t.Fatal("nil driver = nil error, want error")
	}
}

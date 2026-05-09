package core_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/internal/testutil/storefx"
	"github.com/rkurbatov/scrinium/testutil/driverfx"
)

// --- ErrStoreNotFound at fresh Location ---

func TestOpenStore_FreshLocationReturnsNotFound(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, err := storefx.TryOpenOn(t, drv)
	if !errors.Is(err, errs.ErrStoreNotFound) {
		t.Fatalf("expected ErrStoreNotFound, got %v", err)
	}
}

// --- L0/L1 self-heal: missing L0 ---

func TestOpenStore_HealsAbsentL0FromL1(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	// Wipe L0 to simulate a write that completed L1 but not L0.
	if err := drv.Remove(context.Background(), descriptor.Path); err != nil {
		t.Fatalf("setup remove L0: %v", err)
	}

	// OpenStore must succeed (L1 has the canonical descriptor).
	_, err := storefx.TryOpenOn(t, drv)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	// L0 must be back on disk after heal.
	if _, status, err := descriptor.ReadReplica(context.Background(), drv, descriptor.Path); err != nil || status != descriptor.ReplicaValid {
		t.Errorf("L0 should be healed: status=%v, err=%v", status, err)
	}
}

// --- L0/L1 self-heal: missing L1 ---

func TestOpenStore_HealsAbsentL1FromL0(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	if err := drv.Remove(context.Background(), descriptor.BackupPath); err != nil {
		t.Fatalf("setup remove L1: %v", err)
	}

	_ = storefx.OpenOn(t, drv)

	if _, status, err := descriptor.ReadReplica(context.Background(), drv, descriptor.BackupPath); err != nil || status != descriptor.ReplicaValid {
		t.Errorf("L1 should be healed: status=%v, err=%v", status, err)
	}
}

// --- Both replicas absent ---

func TestOpenStore_BothReplicasAbsentReturnsNotFound(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	_ = drv.Remove(context.Background(), descriptor.Path)
	_ = drv.Remove(context.Background(), descriptor.BackupPath)

	_, err := storefx.TryOpenOn(t, drv)
	if !errors.Is(err, errs.ErrStoreNotFound) {
		t.Fatalf("expected ErrStoreNotFound, got %v", err)
	}
}

// --- Both replicas corrupted ---

func TestOpenStore_BothReplicasCorruptedReturnsCorrupted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	// Replace both replicas with garbage that fails Unmarshal.
	if err := drv.Put(context.Background(), descriptor.Path, bytes.NewReader([]byte("not json"))); err != nil {
		t.Fatal(err)
	}
	if err := drv.Put(context.Background(), descriptor.BackupPath, bytes.NewReader([]byte("not json either"))); err != nil {
		t.Fatal(err)
	}

	_, err := storefx.TryOpenOn(t, drv)
	if !errors.Is(err, errs.ErrStoreCorrupted) {
		t.Fatalf("expected ErrStoreCorrupted, got %v", err)
	}
}

// --- Split-brain detection ---

func TestOpenStore_SplitBrainRejected(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	// Read L0, fabricate a divergent L1 with the same Sequence
	// but a different StoreID.
	d0, _, err := descriptor.ReadReplica(context.Background(), drv, descriptor.Path)
	if err != nil {
		t.Fatal(err)
	}
	imposter := *d0
	imposter.StoreID = "99999999-aaaa-bbbb-cccc-dddddddddddd"
	if err := descriptor.WriteReplica(context.Background(), drv, &imposter, descriptor.L1); err != nil {
		t.Fatal(err)
	}

	_, err = storefx.TryOpenOn(t, drv)
	if !errors.Is(err, errs.ErrDescriptorSplitBrain) {
		t.Fatalf("expected ErrDescriptorSplitBrain, got %v", err)
	}
}

// --- Encrypted DEK without AutoUnlock → Locked ---

func TestOpenStore_EncryptedWithoutAutoUnlockGoesLocked(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "hunter2")

	s, err := storefx.TryOpenOn(t, drv)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != domain.StateLocked {
		t.Errorf("State: got %v, want StateLocked", s.State())
	}
}

// --- Encrypted DEK with AutoUnlock → Unlocked ---

func TestOpenStore_EncryptedWithAutoUnlockGoesUnlocked(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "hunter2")

	s, err := storefx.TryOpenOn(t, drv,
		core.WithPassphrase(storefx.StaticPP("hunter2")),
		core.WithAutoUnlock(),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("State: got %v, want StateUnlocked", s.State())
	}
}

// --- AutoUnlock without WithPassphrase ---

func TestOpenStore_AutoUnlockRequiresPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "hunter2")

	_, err := storefx.TryOpenOn(t, drv,
		core.WithAutoUnlock(),
	)
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

// --- AutoUnlock with wrong passphrase ---

func TestOpenStore_AutoUnlockWrongPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "right")

	_, err := storefx.TryOpenOn(t, drv,
		core.WithPassphrase(storefx.StaticPP("wrong")),
		core.WithAutoUnlock(),
	)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

// --- Plain DEK still works (regression) ---

func TestOpenStore_PlainStoreRoundTrip(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)

	s, err := storefx.TryOpenOn(t, drv)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("State: got %v, want StateUnlocked", s.State())
	}
}

// --- L2 cache refresh on first open ---
//
// The first OpenStore on a different host (simulated by a fresh
// in-memory index) should populate the cache from Location. We
// verify by opening twice: first with one index, then we observe
// that no errors occur. The cache itself is package-private; this
// test just ensures the refresh path doesn't crash.
func TestOpenStore_RefreshesL2CacheOnFirstOpen(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)

	// Fresh in-memory index — no L2 cache yet. OpenStore must
	// build it.
	if _, err := storefx.TryOpenOn(t, drv); err != nil {
		t.Fatalf("OpenStore (no cache): %v", err)
	}
}

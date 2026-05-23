package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/storefx"
)

// --- UpdateConfig ---

// TestUpdateConfig_HappyPath verifies that a mutable change lands
// in memory (Config()) and the mutable field arrives at the new
// value. Immutable fields are not touched, so validation passes.
func TestUpdateConfig_HappyPath(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(domain.StoreConfig{
		RetentionPeriod: 2 * time.Hour,
	}))
	ctx := context.Background()

	updated := domain.StoreConfig{
		RetentionPeriod: 24 * time.Hour,
	}
	if err := s.UpdateConfig(ctx, updated); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	got := s.Config()
	if got.RetentionPeriod != 24*time.Hour {
		t.Errorf("RetentionPeriod: got %v, want 24h", got.RetentionPeriod)
	}
}

// TestUpdateConfig_RejectsImmutableChange verifies that a request
// changing PathTopology fails with ErrConfigMismatch and the
// active config is left untouched.
func TestUpdateConfig_RejectsImmutableChange(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(domain.StoreConfig{
		PathTopology: domain.PathTopologyFlat,
	}))
	ctx := context.Background()

	err := s.UpdateConfig(ctx, domain.StoreConfig{
		PathTopology: domain.PathTopologySharded,
	})
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected ErrConfigMismatch, got %v", err)
	}
	if got := s.Config().PathTopology; got != domain.PathTopologyFlat {
		t.Errorf("PathTopology mutated despite rejection: got %q", got)
	}
}

// TestUpdateConfig_RejectsInvalidMutable verifies that the
// requested config is validated by validateImmutableConfig (which
// also covers the value-range checks like RetentionPeriod >= 1h).
// A 30-minute RetentionPeriod is below MinRetentionPeriod and
// must fail with ErrInvalidConfig.
func TestUpdateConfig_RejectsInvalidMutable(t *testing.T) {
	s := storefx.Init(t)
	ctx := context.Background()

	err := s.UpdateConfig(ctx, domain.StoreConfig{
		RetentionPeriod: 30 * time.Minute,
	})
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

// TestUpdateConfig_DeletionPolicyLockGuard verifies that a Store
// initialised with DeletionPolicyLock=true + NoDelete cannot drop
// to a softer policy through UpdateConfig — the lock is real.
func TestUpdateConfig_DeletionPolicyLockGuard(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(domain.StoreConfig{
		DeletionPolicy:     domain.DeletionPolicyNoDelete,
		DeletionPolicyLock: true,
	}))
	ctx := context.Background()

	err := s.UpdateConfig(ctx, domain.StoreConfig{
		DeletionPolicy:     domain.DeletionPolicyFree,
		DeletionPolicyLock: true,
	})
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected ErrConfigMismatch, got %v", err)
	}
}

// TestUpdateConfig_RejectedInReadOnly verifies the state-machine
// guard: write-side admin operations honour MaintenanceModeReadOnly.
func TestUpdateConfig_RejectedInReadOnly(t *testing.T) {
	s := storefx.Init(t)
	ctx := context.Background()
	if err := s.SetMaintenanceMode(ctx, domain.MaintenanceModeReadOnly); err != nil {
		t.Fatalf("SetMaintenanceMode: %v", err)
	}
	err := s.UpdateConfig(ctx, domain.StoreConfig{RetentionPeriod: 5 * time.Hour})
	if !errors.Is(err, errs.ErrStoreReadOnly) {
		t.Fatalf("expected ErrStoreReadOnly, got %v", err)
	}
}

// --- ConfigHistory ---

// TestConfigHistory_SingleSnapshotAfterInit verifies that a Store
// fresh from Init has exactly one entry in ConfigHistory — the
// one written by InitStore itself.
func TestConfigHistory_SingleSnapshotAfterInit(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(domain.StoreConfig{
		RetentionPeriod: 3 * time.Hour,
	}))
	ctx := context.Background()

	hist, err := s.ConfigHistory(ctx)
	if err != nil {
		t.Fatalf("ConfigHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("len: got %d, want 1", len(hist))
	}
	if hist[0].RetentionPeriod != 3*time.Hour {
		t.Errorf("RetentionPeriod: got %v, want 3h", hist[0].RetentionPeriod)
	}
}

// TestConfigHistory_OrdersByCreatedAtDesc verifies that after two
// UpdateConfig calls the history holds three snapshots, the
// active one (latest) at index 0, and the original Init snapshot
// at the tail. RetentionPeriod is the cheap mutable witness.
func TestConfigHistory_OrdersByCreatedAtDesc(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(domain.StoreConfig{
		RetentionPeriod: 1 * time.Hour,
	}))
	ctx := context.Background()

	// Manifest CreatedAt has 1-second resolution per docs/2 §7.5.
	// Sleep before the first UpdateConfig so the Init snapshot is
	// observably older than what follows.
	time.Sleep(1100 * time.Millisecond)
	if err := s.UpdateConfig(ctx, domain.StoreConfig{
		RetentionPeriod: 2 * time.Hour,
	}); err != nil {
		t.Fatalf("UpdateConfig #1: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := s.UpdateConfig(ctx, domain.StoreConfig{
		RetentionPeriod: 4 * time.Hour,
	}); err != nil {
		t.Fatalf("UpdateConfig #2: %v", err)
	}

	hist, err := s.ConfigHistory(ctx)
	if err != nil {
		t.Fatalf("ConfigHistory: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("len: got %d, want 3", len(hist))
	}
	if hist[0].RetentionPeriod != 4*time.Hour {
		t.Errorf("hist[0]: got %v, want 4h (active)", hist[0].RetentionPeriod)
	}
	if hist[len(hist)-1].RetentionPeriod != 1*time.Hour {
		t.Errorf("hist[last]: got %v, want 1h (original)", hist[len(hist)-1].RetentionPeriod)
	}
}

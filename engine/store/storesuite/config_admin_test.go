// Config administration: UpdateConfig (mutable change, plus the rejection
// table — immutable change, invalid mutable, deletion-policy lock, and
// read-only mode) and ConfigHistory (the Init snapshot, and
// newest-first ordering after updates).

package storesuite

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
)

// TestUpdateConfig_HappyPath: a mutable change lands in the active config.
func TestUpdateConfig_HappyPath(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(config.StoreConfig{
		RetentionPeriod: 2 * time.Hour,
	}))
	if err := s.UpdateConfig(context.Background(), config.StoreConfig{
		RetentionPeriod: 24 * time.Hour,
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got := s.Config().RetentionPeriod; got != 24*time.Hour {
		t.Errorf("RetentionPeriod: got %v, want 24h", got)
	}
}

// TestUpdateConfig_Rejected: UpdateConfig refuses an immutable change
// (ErrConfigMismatch, active config untouched), an out-of-range mutable
// (ErrInvalidConfig), a deletion-policy lock being softened
// (ErrConfigMismatch), and any write under read-only mode
// (ErrStoreReadOnly).
func TestUpdateConfig_Rejected(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
		want error
	}{
		{"immutable PathTopology change", func(t *testing.T) error {
			s := storefx.Init(t, store.WithConfig(config.StoreConfig{PathTopology: config.PathTopologyFlat}))
			err := s.UpdateConfig(context.Background(), config.StoreConfig{PathTopology: config.PathTopologySharded})
			if got := s.Config().PathTopology; got != config.PathTopologyFlat {
				t.Errorf("PathTopology mutated despite rejection: got %q", got)
			}
			return err
		}, errs.ErrConfigMismatch},
		{"invalid mutable (retention below min)", func(t *testing.T) error {
			s := storefx.Init(t)
			return s.UpdateConfig(context.Background(), config.StoreConfig{RetentionPeriod: 30 * time.Minute})
		}, errs.ErrInvalidConfig},
		{"deletion-policy lock cannot soften", func(t *testing.T) error {
			s := storefx.Init(t, store.WithConfig(config.StoreConfig{
				DeletionPolicy:     config.DeletionPolicyNoDelete,
				DeletionPolicyLock: true,
			}))
			return s.UpdateConfig(context.Background(), config.StoreConfig{
				DeletionPolicy:     config.DeletionPolicyFree,
				DeletionPolicyLock: true,
			})
		}, errs.ErrConfigMismatch},
		{"read-only maintenance mode", func(t *testing.T) error {
			s := storefx.Init(t)
			if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
				t.Fatalf("SetMaintenanceMode: %v", err)
			}
			return s.UpdateConfig(context.Background(), config.StoreConfig{RetentionPeriod: 5 * time.Hour})
		}, errs.ErrStoreReadOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t); !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestConfigHistory_SingleSnapshotAfterInit: a fresh store has exactly one
// history entry — the one InitStore wrote.
func TestConfigHistory_SingleSnapshotAfterInit(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(config.StoreConfig{
		RetentionPeriod: 3 * time.Hour,
	}))
	hist, err := s.ConfigHistory(context.Background())
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

// TestConfigHistory_OrdersByCreatedAtDesc: after two updates the history
// holds three snapshots, newest (active) at index 0 and the Init snapshot
// at the tail. RetentionPeriod is the mutable witness. (Manifest CreatedAt
// has 1-second resolution per docs/2 §7.5, hence the sleeps.)
func TestConfigHistory_OrdersByCreatedAtDesc(t *testing.T) {
	s := storefx.Init(t, store.WithConfig(config.StoreConfig{
		RetentionPeriod: 1 * time.Hour,
	}))
	ctx := context.Background()

	time.Sleep(1100 * time.Millisecond)
	if err := s.UpdateConfig(ctx, config.StoreConfig{RetentionPeriod: 2 * time.Hour}); err != nil {
		t.Fatalf("UpdateConfig #1: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := s.UpdateConfig(ctx, config.StoreConfig{RetentionPeriod: 4 * time.Hour}); err != nil {
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

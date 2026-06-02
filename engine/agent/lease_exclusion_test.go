package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/lease"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// These are internal (package agent) tests because they reference the
// per-agent lease paths, which are unexported. A squatter on host B
// pre-acquires the lease with a one-hour TTL (live without a
// heartbeat); the agent runs on host A and must refuse with
// ErrLeaseHeld rather than run concurrently.
const (
	exclHostAgent    = "host-a-agent-0001"
	exclHostSquatter = "host-b-squatter-0002"
)

func TestGC_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec),
		store.WithConfig(domain.StoreConfig{GCLeasePolicy: domain.GCLeaseLeaderElection}))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: gcLeasePath, HostID: exclHostSquatter, AgentType: "gc", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire gc lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := NewGCAgent(st, drv, idx, rec, exclHostAgent, "store-gc", GCConfig{})
	if err != nil {
		t.Fatalf("NewGCAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held gc lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

func TestScrub_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: scrubLeasePath, HostID: exclHostSquatter, AgentType: "scrub", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire scrub lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := NewScrubAgent(st, drv, idx, rec, exclHostAgent, "store-scrub", ScrubConfig{Force: true})
	if err != nil {
		t.Fatalf("NewScrubAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held scrub lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

func TestSnapshot_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: snapshotLeasePath, HostID: exclHostSquatter, AgentType: "snapshot", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire snapshot lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := NewSnapshotAgent(st, drv, idx, rec, exclHostAgent, "store-snap", SnapshotConfig{})
	if err != nil {
		t.Fatalf("NewSnapshotAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held snapshot lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

func TestRebuild_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: rebuildLeasePath, HostID: exclHostSquatter, AgentType: "rebuild", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire maintenance lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := NewRebuildIndexAgent(st, drv, idx, rec, exclHostAgent, "store-rebuild", RebuildConfig{})
	if err != nil {
		t.Fatalf("NewRebuildIndexAgent: %v", err)
	}
	// The maintenance lease is the same one Store.RunMaintenance guards
	// with ErrMaintenanceInProgress at the call boundary; the agent
	// itself surfaces the underlying ErrLeaseHeld.
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held maintenance lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

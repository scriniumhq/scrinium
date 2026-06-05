package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent/checkpoint"
	"scrinium.dev/engine/agent/gc"
	"scrinium.dev/engine/agent/internal/lease"
	"scrinium.dev/engine/agent/rebuild"
	"scrinium.dev/engine/agent/scrub"
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
		Path: gc.gcLeasePath, HostID: exclHostSquatter, AgentType: "gc", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire gc lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := gc.NewGCAgent(st, drv, idx, rec, exclHostAgent, "store-gc", gc.GCConfig{})
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
		Path: scrub.scrubLeasePath, HostID: exclHostSquatter, AgentType: "scrub", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire scrub lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := scrub.NewScrubAgent(st, drv, idx, rec, exclHostAgent, "store-scrub", scrub.ScrubConfig{Force: true})
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

func TestCheckpoint_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: checkpoint.checkpointLeasePath, HostID: exclHostSquatter, AgentType: "checkpoint", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire checkpoint lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := checkpoint.NewCheckpointAgent(st, drv, idx, rec, exclHostAgent, "store-snap", checkpoint.CheckpointConfig{})
	if err != nil {
		t.Fatalf("NewCheckpointAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held checkpoint lease err = %v, want ErrLeaseHeld", err)
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
		Path: rebuild.rebuildLeasePath, HostID: exclHostSquatter, AgentType: "rebuild", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire maintenance lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := rebuild.NewRebuildIndexAgent(st, drv, idx, rec, exclHostAgent, "store-rebuild", rebuild.RebuildConfig{})
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

package rebuild

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/lease"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

const (
	exclHostAgent    = "host-a-agent-0001"
	exclHostSquatter = "host-b-squatter-0002"
)

func TestRebuild_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: rebuildLeasePath, HostID: exclHostSquatter, AgentType: "rebuild", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire rebuild lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := NewRebuildIndexAgent(st, drv, idx, rec, exclHostAgent, "store-rebuild", RebuildConfig{})
	if err != nil {
		t.Fatalf("NewRebuildIndexAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held rebuild lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != agent.StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

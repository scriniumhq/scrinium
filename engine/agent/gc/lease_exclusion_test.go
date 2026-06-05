package gc

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
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
	if s, _ := a.Status(); s != agent.StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

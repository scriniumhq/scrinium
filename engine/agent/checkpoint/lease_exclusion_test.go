package checkpoint

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

func TestCheckpoint_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	held, _, err := lease.Acquire(ctx, drv, lease.Config{
		Path: checkpointLeasePath, HostID: exclHostSquatter, AgentType: "checkpoint", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("pre-acquire checkpoint lease: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

	a, err := NewCheckpointAgent(st, drv, idx, rec, exclHostAgent, "store-snap", CheckpointConfig{})
	if err != nil {
		t.Fatalf("NewCheckpointAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held checkpoint lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != agent.StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

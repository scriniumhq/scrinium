package checkpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/leasefx"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// Internal (checkpoint) test: it references the unexported checkpointLeasePath. A
// foreign host holds the lease (staged via leasefx); the agent runs on
// the local host and must refuse with ErrLeaseHeld rather than run
// concurrently.
const exclHostAgent = "host-a-agent-0001"

func TestCheckpoint_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	leasefx.StageForeign(t, drv, checkpointLeasePath, "host-b-squatter-0002", "checkpoint", time.Hour)

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

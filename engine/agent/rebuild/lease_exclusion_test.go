package rebuild

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/leasefx"
	"scrinium.dev/testutil/storefx"
)

// Internal (rebuild) test: it references the unexported rebuildLeasePath. A
// foreign host holds the lease (staged via leasefx); the agent runs on
// the local host and must refuse with ErrLeaseHeld rather than run
// concurrently.
const exclHostAgent = "host-a-agent-0001"

func TestRebuild_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	leasefx.StageForeign(t, drv, rebuildLeasePath, "host-b-squatter-0002", "rebuild", time.Hour)

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

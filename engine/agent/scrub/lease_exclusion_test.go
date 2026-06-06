package scrub

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

// Internal (scrub) test: it references the unexported scrubLeasePath. A
// foreign host holds the lease (staged via leasefx); the agent runs on
// the local host and must refuse with ErrLeaseHeld rather than run
// concurrently.
const exclHostAgent = "host-a-agent-0001"

func TestScrub_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	ctx := context.Background()

	leasefx.StageForeign(t, drv, scrubLeasePath, "host-b-squatter-0002", "scrub", time.Hour)

	a, err := NewScrubAgent(st, drv, idx, rec, exclHostAgent, "store-scrub", ScrubConfig{Force: true})
	if err != nil {
		t.Fatalf("NewScrubAgent: %v", err)
	}
	if _, err := a.Run(ctx); !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Run under held scrub lease err = %v, want ErrLeaseHeld", err)
	}
	if s, _ := a.Status(); s != agent.StateFaulted {
		t.Errorf("state = %v, want StateFaulted", s)
	}
}

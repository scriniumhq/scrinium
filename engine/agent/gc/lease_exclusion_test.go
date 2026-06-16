package gc

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/leasefx"
	"scrinium.dev/testutil/storefx"
)

// Internal (gc) test: it references the unexported gcLeasePath. A
// foreign host holds the lease (staged via leasefx); the agent runs on
// the local host and must refuse with ErrLeaseHeld rather than run
// concurrently.
const exclHostAgent = "host-a-agent-0001"

func TestGC_LeaseExclusion(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec),
		store.WithConfig(domain.StoreConfig{GCLeasePolicy: domain.GCLeaseLeaderElection}))
	ctx := context.Background()

	leasefx.StageForeign(t, drv, gcLeasePath, "host-b-squatter-0002", "gc", time.Hour)

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

package gc_test

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/gc"
	"scrinium.dev/testutil/schedfx"
)

// TestGC_Scheduled verifies the Scheduler builds the registered gc agent
// from the registry (with the kind-specific Config) and invokes it on a
// due Tick, and that the scheduled run completes without failure.
// Agent-internal behaviour is covered by gc's own tests.
func TestGC_Scheduled(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	f.putAndOrphan(t, "data")

	h := schedfx.New(t, f.store, f.drv, f.idx, f.rec, "store-gc")
	h.MustAdd(t, agent.Schedule{Agent: "gc", Interval: time.Minute, Config: gc.GCConfig{}})

	// Wait for the run to finish on its own; cancelling via Stop here
	// would race a slow agent and emit a spurious failure (see schedfx).
	h.TickAndWaitDone(t, time.Now(), "gc", 5*time.Second)
	if n := schedfx.CountFailed(h.Rec, "gc"); n != 0 {
		t.Errorf("gc emitted %d failure events during scheduled run, want 0", n)
	}
}

// TestGC_CustomSchedulerUsesRunMaintenance demonstrates the always-on
// foundation (ADR-69): a host with its own scheduler does not use
// agent.Scheduler at all — it builds the agent and calls
// Store.RunMaintenance from its own loop. Our Scheduler is optional sugar
// over this path and is fully replaceable by the host's own driver.
func TestGC_CustomSchedulerUsesRunMaintenance(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	a := newGC(t, f, gc.GCConfig{})

	// The host's own scheduler would call this from its loop; no
	// agent.Scheduler is involved.
	if _, err := f.store.RunMaintenance(context.Background(), a); err != nil {
		t.Fatalf("RunMaintenance (custom loop): %v", err)
	}
	if got := schedfx.CountStarted(f.rec, "gc"); got < 1 {
		t.Fatalf("gc started %d times via custom loop, want >= 1", got)
	}
}

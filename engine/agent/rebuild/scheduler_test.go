package rebuild_test

import (
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/schedfx"
	"scrinium.dev/engine/agent/rebuild"
)

// TestRebuild_Scheduled verifies the Scheduler builds and invokes the
// registered rebuild agent on a due Tick and that the scheduled run
// completes without failure. deps.Index is the rebuild target
// (f.rebuilt); the default Source (FullScan) passes Validate.
func TestRebuild_Scheduled(t *testing.T) {
	f := newRebuildFixture(t)
	f.put(t, "r", "rebuild payload data")

	h := schedfx.New(t, f.store, f.drv, f.rebuilt, f.rec, "store-rebuild")
	h.MustAdd(t, agent.Schedule{Agent: "rebuild", Interval: time.Minute, Config: rebuild.RebuildConfig{}})

	h.TickAndWaitStarted(t, time.Now(), "rebuild", 1, time.Second)
	h.StopAndWait(t)
	if n := schedfx.CountFailed(h.Rec, "rebuild"); n != 0 {
		t.Errorf("rebuild emitted %d failure events during scheduled run, want 0", n)
	}
}

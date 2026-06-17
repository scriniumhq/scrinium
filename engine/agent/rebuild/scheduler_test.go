package rebuild_test

import (
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/rebuild"
	"scrinium.dev/testutil/schedfx"
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

	// Wait for the run to finish on its own; cancelling via Stop here
	// would race a slow agent and emit a spurious failure (see schedfx).
	h.TickAndWaitDone(t, time.Now(), "rebuild", 5*time.Second)
	if n := schedfx.CountFailed(h.Rec, "rebuild"); n != 0 {
		t.Errorf("rebuild emitted %d failure events during scheduled run, want 0", n)
	}
}

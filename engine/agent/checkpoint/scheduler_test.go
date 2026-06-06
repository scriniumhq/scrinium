package checkpoint_test

import (
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/checkpoint"
	"scrinium.dev/engine/agent/internal/schedfx"
)

// TestCheckpoint_Scheduled verifies the Scheduler builds and invokes the
// registered checkpoint agent on a due Tick and that the scheduled run
// completes without failure.
func TestCheckpoint_Scheduled(t *testing.T) {
	f := newCheckpointFixture(t)
	f.put(t, "payload")

	h := schedfx.New(t, f.store, f.drv, f.idx, f.rec, "store-snap")
	h.MustAdd(t, agent.Schedule{Agent: "checkpoint", Interval: time.Minute, Config: checkpoint.CheckpointConfig{}})

	// Wait for the run to finish on its own; cancelling via Stop here
	// would race a slow agent and emit a spurious failure (see schedfx).
	h.TickAndWaitDone(t, time.Now(), "checkpoint", 5*time.Second)
	if n := schedfx.CountFailed(h.Rec, "checkpoint"); n != 0 {
		t.Errorf("checkpoint emitted %d failure events during scheduled run, want 0", n)
	}
}

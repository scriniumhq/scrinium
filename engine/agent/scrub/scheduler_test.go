package scrub_test

import (
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/schedfx"
	"scrinium.dev/engine/agent/scrub"
)

// TestScrub_Scheduled verifies the Scheduler builds and invokes the
// registered scrub agent on a due Tick and that the scheduled run
// completes without failure.
func TestScrub_Scheduled(t *testing.T) {
	f := newScrubFixture(t)
	f.put(t, "v", "data")

	h := schedfx.New(t, f.store, f.drv, f.idx, f.rec, "store-scrub")
	h.MustAdd(t, agent.Schedule{Agent: "scrub", Interval: time.Minute, Config: scrub.ScrubConfig{Force: true}})

	h.TickAndWaitStarted(t, time.Now(), "scrub", 1, time.Second)
	h.StopAndWait(t)
	if n := schedfx.CountFailed(h.Rec, "scrub"); n != 0 {
		t.Errorf("scrub emitted %d failure events during scheduled run, want 0", n)
	}
}

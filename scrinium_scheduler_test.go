package scrinium_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	scrinium "scrinium.dev"
	"scrinium.dev/engine/agent/gc"
	"scrinium.dev/event"
)

// WithStandardScheduler runs a due agent on its own goroutine: a tiny
// interval is due on the first tick, and the agent's started event
// arrives without the test driving any clock. (Scheduling semantics —
// interval gating, overlap-skip — are unit-tested on the primitive with
// an injected Tick; here we only prove the client's ticker is wired.)
func TestFacade_StandardScheduler_RunsAgent(t *testing.T) {
	ctx := context.Background()
	var started atomic.Int64
	c, err := scrinium.Open(ctx, "file://"+t.TempDir(),
		scrinium.WithStandardScheduler(),
		scrinium.WithEventHandler(func(e scrinium.Event) {
			if e.Type == event.EventAgentStarted {
				started.Add(1)
			}
		}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.ScheduleEvery("gc", time.Millisecond, gc.GCConfig{}); err != nil {
		t.Fatalf("ScheduleEvery: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if started.Load() >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("standard scheduler did not run gc within timeout (started=%d)", started.Load())
}

// ScheduleEvery without WithStandardScheduler has no scheduler to drive
// the schedule and must report that, rather than silently dropping it.
func TestFacade_ScheduleEvery_RequiresStandardScheduler(t *testing.T) {
	ctx := context.Background()
	c, err := scrinium.Open(ctx, "file://"+t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.ScheduleEvery("gc", time.Hour, gc.GCConfig{}); err == nil {
		t.Fatal("ScheduleEvery without WithStandardScheduler = nil, want error")
	}
}

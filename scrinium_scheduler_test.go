package scrinium_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	scrinium "scrinium.dev"
	"scrinium.dev/engine/agent/cron"
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

// WithSchedule declares a schedule at build time; declaring one raises the
// scheduler even without WithStandardScheduler (§9.7 by-config/by-call), and
// the agent runs on its cadence. We pass the config via WithAgentConfig.
func TestFacade_WithSchedule_RaisesSchedulerAndRuns(t *testing.T) {
	ctx := context.Background()
	var started atomic.Int64
	c, err := scrinium.Open(ctx, "file://"+t.TempDir(),
		scrinium.WithSchedule("gc", "1ms"),
		scrinium.WithAgentConfig("gc", gc.GCConfig{}),
		scrinium.WithEventHandler(func(e scrinium.Event) {
			if e.Type == event.EventAgentStarted {
				started.Add(1)
			}
		}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if started.Load() >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WithSchedule did not run gc within timeout (started=%d)", started.Load())
}

// A cron schedule (not a duration) without cron.Enable is a fail-fast
// build error, not a silent no-op.
func TestFacade_WithSchedule_CronWithoutEnable_Errors(t *testing.T) {
	ctx := context.Background()
	_, err := scrinium.Open(ctx, "file://"+t.TempDir(),
		scrinium.WithSchedule("gc", "0 3 * * *"))
	if err == nil {
		t.Fatal("cron WithSchedule without cron.Enable = nil, want fail-fast error")
	}
	if !strings.Contains(err.Error(), "cron") {
		t.Errorf("error = %v, want one mentioning cron", err)
	}
}

// cron.Enable + WithStandardScheduler: a cron schedule actually runs the
// agent on the client's ticker. "@every 1s" keeps the test quick.
func TestFacade_ScheduleCron_Runs(t *testing.T) {
	ctx := context.Background()
	var started atomic.Int64
	c, err := scrinium.Open(ctx, "file://"+t.TempDir(),
		scrinium.WithStandardScheduler(), cron.Enable(),
		scrinium.WithEventHandler(func(e scrinium.Event) {
			if e.Type == event.EventAgentStarted {
				started.Add(1)
			}
		}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.ScheduleCron("gc", "@every 1s", gc.GCConfig{}); err != nil {
		t.Fatalf("ScheduleCron: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if started.Load() >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("cron schedule did not run gc within timeout (started=%d)", started.Load())
}

// ScheduleCron without cron.Enable has no parser and must report it.
func TestFacade_ScheduleCron_RequiresCronAdapter(t *testing.T) {
	ctx := context.Background()
	c, err := scrinium.Open(ctx, "file://"+t.TempDir(), scrinium.WithStandardScheduler())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.ScheduleCron("gc", "@every 1s", gc.GCConfig{}); err == nil {
		t.Fatal("ScheduleCron without cron.Enable = nil, want error")
	}
}

// An invalid cron expression is reported by ScheduleCron, not swallowed.
func TestFacade_ScheduleCron_InvalidExpr(t *testing.T) {
	ctx := context.Background()
	c, err := scrinium.Open(ctx, "file://"+t.TempDir(), scrinium.WithStandardScheduler(), cron.Enable())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.ScheduleCron("gc", "not a cron expr", gc.GCConfig{}); err == nil {
		t.Fatal("ScheduleCron with invalid expr = nil, want error")
	}
}

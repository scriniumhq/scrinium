package scrinium_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	scrinium "scrinium.dev"
	"scrinium.dev/engine/agent/cron"
	"scrinium.dev/engine/agent/gc"
	"scrinium.dev/event"
)

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

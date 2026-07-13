//go:build e2e

package e2e

import (
	"context"
	"testing"

	scrinium "scrinium.dev"
	"scrinium.dev/engine/agent/gc"
	"scrinium.dev/event"
)

// A post-build Subscribe receives agent events from a manual
// RunMaintenance, and the returned unsubscribe stops further delivery.
// The bus is synchronous, so by the time RunMaintenance returns the
// handler has already run on the same goroutine — no synchronisation
// needed around the counter.
func TestFacade_Subscribe_ReceivesAgentEvents(t *testing.T) {
	ctx := context.Background()
	c := openFacade(t)

	started := 0
	unsub := c.Subscribe(func(e scrinium.Event) {
		if e.Type == event.EventAgentStarted {
			started++
		}
	})

	if _, err := c.RunMaintenance(ctx, "gc", gc.GCConfig{}); err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if started < 1 {
		t.Fatalf("subscriber saw %d %q events, want >= 1", started, event.EventAgentStarted)
	}

	unsub()
	started = 0
	if _, err := c.RunMaintenance(ctx, "gc", gc.GCConfig{}); err != nil {
		t.Fatalf("RunMaintenance after unsub: %v", err)
	}
	if started != 0 {
		t.Errorf("after unsubscribe saw %d events, want 0", started)
	}
}

// WithEventHandler installs a handler before assembly; it is on the same
// bus and receives later agent events (the build-phase guarantee is the
// same path, just attached earlier).
func TestFacade_WithEventHandler_ReceivesAgentEvents(t *testing.T) {
	ctx := context.Background()
	started := 0
	c, err := scrinium.Open(ctx, "file://"+t.TempDir(),
		scrinium.WithEventHandler(func(e scrinium.Event) {
			if e.Type == event.EventAgentStarted {
				started++
			}
		}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.RunMaintenance(ctx, "gc", gc.GCConfig{}); err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if started < 1 {
		t.Fatalf("WithEventHandler saw %d %q events, want >= 1", started, event.EventAgentStarted)
	}
}

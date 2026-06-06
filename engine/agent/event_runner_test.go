package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"scrinium.dev/event"
	"scrinium.dev/testutil/storefx"
)

func TestNewEventRunner_NilArgs(t *testing.T) {
	if _, err := NewEventRunner(nil, AgentDeps{}, event.NewEventBus()); err == nil {
		t.Error("nil store = nil err, want error")
	}
	st, _, _ := storefx.InitShared(t)
	if _, err := NewEventRunner(st, AgentDeps{}, nil); err == nil {
		t.Error("nil bus = nil err, want error")
	}
}

func TestEventRunner_FiresEveryNth(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	st, _, _ := storefx.InitShared(t)
	bus := event.NewEventBus()
	r, err := NewEventRunner(st, AgentDeps{}, bus)
	if err != nil {
		t.Fatalf("NewEventRunner: %v", err)
	}
	defer r.Stop(context.Background())
	if err := r.On("test.tick", 3, "sched-test", nil); err != nil {
		t.Fatalf("On: %v", err)
	}

	// Two events: below threshold, no run.
	bus.Publish(event.Event{Type: "test.tick"})
	bus.Publish(event.Event{Type: "test.tick"})
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 0 {
		t.Fatalf("after 2 events run count = %d, want 0", n)
	}

	// Third event: threshold reached, one run.
	bus.Publish(event.Event{Type: "test.tick"})
	waitRunCount(t, 1, time.Second)
	time.Sleep(30 * time.Millisecond) // let the run clear before the next window

	// Three more events: a second run.
	bus.Publish(event.Event{Type: "test.tick"})
	bus.Publish(event.Event{Type: "test.tick"})
	bus.Publish(event.Event{Type: "test.tick"})
	waitRunCount(t, 2, time.Second)
}

func TestEventRunner_IgnoresOtherTypes(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	st, _, _ := storefx.InitShared(t)
	bus := event.NewEventBus()
	r, err := NewEventRunner(st, AgentDeps{}, bus)
	if err != nil {
		t.Fatalf("NewEventRunner: %v", err)
	}
	defer r.Stop(context.Background())
	if err := r.On("test.tick", 1, "sched-test", nil); err != nil {
		t.Fatalf("On: %v", err)
	}
	// A different event type must not advance the counter.
	bus.Publish(event.Event{Type: "other.event"})
	bus.Publish(event.Event{Type: "other.event"})
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 0 {
		t.Fatalf("unrelated events triggered %d runs, want 0", n)
	}
}

func TestEventRunner_SkipsInFlight(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	st, _, _ := storefx.InitShared(t)
	bus := event.NewEventBus()
	r, err := NewEventRunner(st, AgentDeps{}, bus)
	if err != nil {
		t.Fatalf("NewEventRunner: %v", err)
	}
	gate := make(chan struct{})
	if err := r.On("test.tick", 1, "sched-test", schedTestConfig{gate: gate}); err != nil {
		t.Fatalf("On: %v", err)
	}

	// First event fires a run that blocks on the gate.
	bus.Publish(event.Event{Type: "test.tick"})
	waitRunCount(t, 1, time.Second)

	// Second event crosses the threshold again while the first run is in
	// flight: skipped, not queued.
	bus.Publish(event.Event{Type: "test.tick"})
	time.Sleep(30 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 1 {
		t.Fatalf("in-flight skip: run count = %d, want 1", n)
	}

	close(gate) // let the first run finish
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestEventRunner_StopUnsubscribes(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	st, _, _ := storefx.InitShared(t)
	bus := event.NewEventBus()
	r, err := NewEventRunner(st, AgentDeps{}, bus)
	if err != nil {
		t.Fatalf("NewEventRunner: %v", err)
	}
	if err := r.On("test.tick", 1, "sched-test", nil); err != nil {
		t.Fatalf("On: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop the runner is unsubscribed: events no longer fire it.
	bus.Publish(event.Event{Type: "test.tick"})
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 0 {
		t.Fatalf("after Stop run count = %d, want 0", n)
	}

	// Stop is idempotent.
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop (second) = %v, want nil", err)
	}
}

func TestEventRunner_OnValidation(t *testing.T) {
	st, _, _ := storefx.InitShared(t)
	bus := event.NewEventBus()
	r, err := NewEventRunner(st, AgentDeps{}, bus)
	if err != nil {
		t.Fatalf("NewEventRunner: %v", err)
	}
	defer r.Stop(context.Background())

	if err := r.On("test.tick", 0, "sched-test", nil); err == nil {
		t.Error("On with every=0 = nil, want error")
	}
	if err := r.On("test.tick", 3, "no-such-agent", nil); err == nil {
		t.Error("On with unregistered kind = nil, want error")
	}
	if err := r.On("", 3, "sched-test", nil); err == nil {
		t.Error("On with empty event type = nil, want error")
	}
}

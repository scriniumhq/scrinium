// Package schedfx provides test fixtures for the agent Scheduler (ADR-69).
//
// It lives under engine/agent/internal so agent subpackage tests can
// import it. Runs are observed through a recorder: every built-in agent
// emits EventAgentStarted at the top of Run, so counting those events for
// an AgentType is a reliable "the scheduler built and invoked this agent"
// signal, independent of what the agent does internally.
package schedfx

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/event"
	"scrinium.dev/testutil/eventfx"
)

// HostID is the synthetic host scheduled agents run under in tests.
const HostID = "schedfx-host-0001"

// CountStarted returns how many EventAgentStarted events rec captured for
// agentType. Usable without a Harness — e.g. when driving an agent
// through Store.RunMaintenance directly (a host's own scheduler).
func CountStarted(rec *eventfx.Recorder, agentType string) int {
	n := 0
	for _, e := range rec.ByType(event.EventAgentStarted) {
		if p, ok := e.Payload.(event.AgentStartedPayload); ok && p.AgentType == agentType {
			n++
		}
	}
	return n
}

// CountFailed returns how many EventAgentFailed events rec captured for
// agentType. Paired with CountStarted it turns a scheduled run into a
// "started and did not fail" assertion.
func CountFailed(rec *eventfx.Recorder, agentType string) int {
	n := 0
	for _, e := range rec.ByType(event.EventAgentFailed) {
		if p, ok := e.Payload.(event.AgentFailedPayload); ok && p.AgentType == agentType {
			n++
		}
	}
	return n
}

// Harness drives a real agent.Scheduler over a test store and observes
// runs through rec — the same recorder the store (and therefore the
// scheduled agents, via AgentDeps.Publisher) publish to.
type Harness struct {
	Sched agent.Scheduler
	Rec   *eventfx.Recorder
}

// New builds a Scheduler over st with deps wired to rec, drv and idx, so
// agents the scheduler builds publish into the same recorder. The
// Scheduler is stopped on test cleanup.
func New(t *testing.T, st store.Store, drv driver.Driver, idx index.StoreIndex, rec *eventfx.Recorder, storeID string) *Harness {
	t.Helper()
	s, err := agent.NewScheduler(st, agent.AgentDeps{
		Publisher: rec,
		Driver:    drv,
		Index:     idx,
		HostID:    HostID,
		StoreID:   storeID,
	})
	if err != nil {
		t.Fatalf("schedfx.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return &Harness{Sched: s, Rec: rec}
}

// MustAdd registers a schedule, failing the test on error.
func (h *Harness) MustAdd(t *testing.T, s agent.Schedule) {
	t.Helper()
	if err := h.Sched.Add(s); err != nil {
		t.Fatalf("schedfx Add(%q): %v", s.Agent, err)
	}
}

// StopAndWait stops the scheduler and blocks until in-flight runs drain,
// so callers can assert on terminal events (e.g. CountFailed) without
// racing the async run. Safe to call before the cleanup Stop (which is
// idempotent).
func (h *Harness) StopAndWait(t *testing.T) {
	t.Helper()
	if err := h.Sched.Stop(context.Background()); err != nil {
		t.Fatalf("schedfx Stop: %v", err)
	}
}

// TickAndWaitStarted ticks at now, then polls until agentType has at
// least want Started events (agents run asynchronously) or fails after
// timeout.
func (h *Harness) TickAndWaitStarted(t *testing.T, now time.Time, agentType string, want int, timeout time.Duration) {
	t.Helper()
	if err := h.Sched.Tick(now); err != nil {
		t.Fatalf("schedfx Tick: %v", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if CountStarted(h.Rec, agentType) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("agent %q started %d times, want >= %d within %s",
		agentType, CountStarted(h.Rec, agentType), want, timeout)
}

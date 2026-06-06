package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/storefx"
)

// --- fake agent registered once for scheduler tests ---

// schedRunCount counts Run invocations across all schedTestAgent
// instances. Tests reset it to 0 at their start; the package test binary
// runs these sequentially (no t.Parallel), so the shared counter is safe.
var schedRunCount int64

// schedTestConfig is the kind-specific config the fake factory decodes.
// A non-nil gate makes Run block until the gate is closed or the context
// is cancelled — used to exercise overlap-skip and Stop-cancels-in-flight.
type schedTestConfig struct {
	gate chan struct{}
}

type schedTestAgent struct {
	cfg schedTestConfig
}

func (a *schedTestAgent) AgentType() string                  { return "sched-test" }
func (a *schedTestAgent) Validate(ctx context.Context) error { return ctx.Err() }
func (a *schedTestAgent) Status() (State, error)             { return StateIdle, nil }

func (a *schedTestAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	atomic.AddInt64(&schedRunCount, 1)
	if a.cfg.gate != nil {
		select {
		case <-a.cfg.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &domain.AgentResult{AgentType: "sched-test"}, nil
}

type schedTestFactory struct{}

func (schedTestFactory) Name() string { return "sched-test" }
func (schedTestFactory) Build(st store.Store, cfg any, deps AgentDeps) (Agent, error) {
	c, _ := cfg.(schedTestConfig)
	return &schedTestAgent{cfg: c}, nil
}

func init() { Register(schedTestFactory{}) }

// waitRunCount polls schedRunCount until it reaches want or the deadline
// elapses. Polling (not real timers in the code under test) keeps the
// Scheduler itself time-agnostic; only the assertion waits.
func waitRunCount(t *testing.T, want int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&schedRunCount) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run count = %d, want %d within %s", atomic.LoadInt64(&schedRunCount), want, within)
}

func newSchedFixture(t *testing.T) (Scheduler, store.Store) {
	t.Helper()
	st, _, _ := storefx.InitShared(t)
	s, err := NewScheduler(st, AgentDeps{})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	return s, st
}

func TestScheduler_TickRunsDueAgent(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Minute}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Tick(time.Now()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 1, time.Second)
}

func TestScheduler_TickSkipsBeforeInterval(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Hour}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t0 := time.Now()
	if err := s.Tick(t0); err != nil {
		t.Fatalf("Tick #1: %v", err)
	}
	waitRunCount(t, 1, time.Second)

	// A minute later is still well within the hour interval: no new run.
	if err := s.Tick(t0.Add(time.Minute)); err != nil {
		t.Fatalf("Tick #2: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt64(&schedRunCount); got != 1 {
		t.Errorf("run count = %d, want 1 (second tick within interval must not run)", got)
	}
}

func TestScheduler_TickRunsAgainAfterInterval(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Minute}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t0 := time.Now()
	if err := s.Tick(t0); err != nil {
		t.Fatalf("Tick #1: %v", err)
	}
	waitRunCount(t, 1, time.Second)

	// Two minutes later the interval has elapsed: a second run fires.
	if err := s.Tick(t0.Add(2 * time.Minute)); err != nil {
		t.Fatalf("Tick #2: %v", err)
	}
	waitRunCount(t, 2, time.Second)
}

func TestScheduler_OverlapSkipped(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	gate := make(chan struct{})
	if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Nanosecond, Config: schedTestConfig{gate: gate}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t0 := time.Now()
	if err := s.Tick(t0); err != nil {
		t.Fatalf("Tick #1: %v", err)
	}
	waitRunCount(t, 1, time.Second) // first run is now blocked on the gate

	// Even though the interval has elapsed, the entry is in-flight, so a
	// second tick must skip it rather than start a concurrent run.
	if err := s.Tick(t0.Add(time.Hour)); err != nil {
		t.Fatalf("Tick #2: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt64(&schedRunCount); got != 1 {
		t.Errorf("run count = %d, want 1 (overlapping tick must be skipped)", got)
	}

	close(gate) // release the in-flight run
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestScheduler_StopCancelsInFlight(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	gate := make(chan struct{}) // never closed; only ctx-cancel frees the run
	if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Nanosecond, Config: schedTestConfig{gate: gate}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Tick(time.Now()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 1, time.Second) // run is now blocked on the gate

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop did not drain the cancelled in-flight run: %v", err)
	}
}

func TestScheduler_AddValidation(t *testing.T) {
	s, _ := newSchedFixture(t)
	if err := s.Add(Schedule{Agent: "no-such-agent", Interval: time.Minute}); err == nil {
		t.Error("Add(unregistered agent) = nil, want error")
	}
	if err := s.Add(Schedule{Agent: "sched-test", Interval: 0}); err == nil {
		t.Error("Add(zero interval) = nil, want error")
	}
}

func TestScheduler_NilStore(t *testing.T) {
	if _, err := NewScheduler(nil, AgentDeps{}); err == nil {
		t.Fatal("NewScheduler(nil store) = nil, want error")
	}
}

func TestScheduler_MultipleDueInOneTick(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	// Two independent entries, both due on the first Tick (lastRun zero);
	// a single Tick must dispatch both, not just the first.
	for i := 0; i < 2; i++ {
		if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Minute}); err != nil {
			t.Fatalf("Add #%d: %v", i, err)
		}
	}
	if err := s.Tick(time.Now()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 2, time.Second)
}

func TestScheduler_TickAndAddAfterStop(t *testing.T) {
	s, _ := newSchedFixture(t)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := s.Tick(time.Now()); err == nil {
		t.Error("Tick after Stop = nil, want error")
	}
	if err := s.Add(Schedule{Agent: "sched-test", Interval: time.Minute}); err == nil {
		t.Error("Add after Stop = nil, want error")
	}
}
func TestScheduler_CronGateFiresAtScheduledMoment(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	hourly := func(prev time.Time) time.Time { return prev.Truncate(time.Hour).Add(time.Hour) }
	if err := s.Add(Schedule{Agent: "sched-test", Next: hourly}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// 00:30 — establishes the schedule (next moment 01:00); no fire, and
	// notably no fire-on-add.
	if err := s.Tick(base.Add(30 * time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 0 {
		t.Fatalf("before scheduled moment ran %d times, want 0", n)
	}
	// 01:00 — fires.
	if err := s.Tick(base.Add(time.Hour)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 1, time.Second)
	// 01:30 — before the next scheduled moment (02:00): no further fire.
	if err := s.Tick(base.Add(90 * time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 1 {
		t.Fatalf("between scheduled moments ran %d times, want 1", n)
	}
	// 02:00 — fires again.
	if err := s.Tick(base.Add(2 * time.Hour)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 2, time.Second)
}

func TestScheduler_CronGateDriftFree(t *testing.T) {
	atomic.StoreInt64(&schedRunCount, 0)
	s, _ := newSchedFixture(t)
	hourly := func(prev time.Time) time.Time { return prev.Truncate(time.Hour).Add(time.Hour) }
	if err := s.Add(Schedule{Agent: "sched-test", Next: hourly}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Establish next moment = 01:00.
	if err := s.Tick(base.Add(30 * time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	// Ticker "slept" past 01:00 and wakes at 01:00:02 — fires late (once).
	if err := s.Tick(base.Add(time.Hour + 2*time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 1, time.Second)
	// The next moment must be the scheduled 02:00, not 02:00:02: a tick
	// just before 02:00 must not fire.
	if err := s.Tick(base.Add(2*time.Hour - time.Second)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt64(&schedRunCount); n != 1 {
		t.Fatalf("before 02:00 ran %d times, want 1 (a drifted schedule would have fired)", n)
	}
	if err := s.Tick(base.Add(2 * time.Hour)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitRunCount(t, 2, time.Second)
}

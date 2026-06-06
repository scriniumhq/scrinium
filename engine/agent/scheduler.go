package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
)

// Schedule is one entry in a Scheduler: which registered agent to invoke
// and how often. Config is the kind-specific configuration handed to the
// agent's factory (for example GCConfig); a nil or zero value selects the
// agent's own defaults.
//
// ADR-69 sketches the entry as {Agent, Interval}; Config is added here
// because the Scheduler builds the agent fresh on each run (agents are
// one-shot, ADR-68) and must know what to build it with.
type Schedule struct {
	// Agent is the AgentType of a registered agent (agent.Lookup).
	Agent string
	// Interval is the minimum period between runs of this entry. Ignored
	// when Next is set.
	Interval time.Duration
	// Config is the kind-specific config for the agent's factory.
	Config any
	// Next, when non-nil, overrides interval gating: it returns the next
	// run time strictly after prev (wall-clock, e.g. cron), and the
	// scheduler fires when that moment passes. The scheduler stays
	// expression-syntax agnostic — it only calls Next. nil = interval.
	Next func(prev time.Time) time.Time
}

// CronParser turns a schedule expression into a Schedule.Next function,
// or an error if the expression is invalid. The scheduler primitive does
// not depend on any cron library; an adapter package
// (scrinium.dev/engine/agent/cron) supplies the implementation.
type CronParser func(expr string) (next func(prev time.Time) time.Time, err error)

// Scheduler is the time-driven launch primitive (ADR-69). It does not
// own time: the owner calls Tick(now) from its own loop, so an embedding
// host never gets a hidden timer goroutine. The only resident goroutines
// are the short-lived ones a Tick spawns for due agents, joined by Stop.
//
// Overlap is prevented on two levels: within one Scheduler an in-flight
// entry is skipped, not queued; across processes the agent's own
// maintenance lease makes a concurrent run fail fast, which the Scheduler
// treats as an ordinary skipped cycle. Stop cancels the context handed to
// in-flight agents and waits for them to return.
type Scheduler interface {
	// Add registers a schedule. The agent type must be registered
	// (agent.Lookup) and the interval must be positive.
	Add(s Schedule) error

	// Tick runs every entry due at now, asynchronously, and returns
	// only on a scheduler-level error. Per-agent failures surface
	// through the agent's own events (EventAgentFailed), not here.
	Tick(now time.Time) error

	// Stop cancels in-flight runs and waits for them, bounded by ctx.
	// Idempotent.
	Stop(ctx context.Context) error
}

type schedEntry struct {
	sched   Schedule
	lastRun time.Time
	nextDue time.Time // for Next-driven (cron) entries: next scheduled fire
	running bool
}

type scheduler struct {
	st   store.Store
	deps AgentDeps
	log  *slog.Logger

	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu      sync.Mutex
	entries []*schedEntry
	stopped bool
}

var _ Scheduler = (*scheduler)(nil)

// NewScheduler builds a Scheduler that launches agents over st with deps.
// It holds no timer; the caller drives periodicity through Tick.
func NewScheduler(st store.Store, deps AgentDeps, opts ...AgentOption) (Scheduler, error) {
	if st == nil {
		return nil, fmt.Errorf("agent.NewScheduler: nil store")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &scheduler{
		st:      st,
		deps:    deps,
		log:     resolveAgentLogger(opts),
		baseCtx: ctx,
		cancel:  cancel,
	}, nil
}

func (s *scheduler) Add(sc Schedule) error {
	if !validAgentType(sc.Agent) {
		return fmt.Errorf("%w: %q", errs.ErrInvalidAgentType, sc.Agent)
	}
	if _, ok := Lookup(sc.Agent); !ok {
		return fmt.Errorf("%w: no agent registered for %q", errs.ErrInvalidAgentType, sc.Agent)
	}
	if sc.Next == nil && sc.Interval <= 0 {
		return fmt.Errorf("agent.Scheduler.Add: interval must be positive for %q", sc.Agent)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return fmt.Errorf("agent.Scheduler.Add: scheduler stopped")
	}
	s.entries = append(s.entries, &schedEntry{sched: sc})
	return nil
}

func (s *scheduler) Tick(now time.Time) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return fmt.Errorf("agent.Scheduler.Tick: scheduler stopped")
	}
	var due []*schedEntry
	for _, e := range s.entries {
		if e.running {
			continue // skip, don't queue (ADR-69)
		}
		fire := false
		if e.sched.Next != nil {
			// Wall-clock (cron) gating, drift-free: nextDue advances from
			// the scheduled moment, not from the actual run time.
			if e.nextDue.IsZero() {
				e.nextDue = e.sched.Next(now) // first scheduled moment after now
			}
			if !e.nextDue.IsZero() && !now.Before(e.nextDue) {
				fire = true
				// Advance to the first scheduled moment strictly after now:
				// one catch-up run for moments missed while the ticker
				// slept, not one run per missed moment.
				next := e.sched.Next(e.nextDue)
				for !next.IsZero() && !next.After(now) {
					next = e.sched.Next(next)
				}
				e.nextDue = next
			}
		} else if e.lastRun.IsZero() || now.Sub(e.lastRun) >= e.sched.Interval {
			fire = true
		}
		if fire {
			e.running = true
			e.lastRun = now
			due = append(due, e)
		}
	}
	s.mu.Unlock()

	for _, e := range due {
		e := e
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runOne(e)
		}()
	}
	return nil
}

// runOne builds the agent fresh (agents are one-shot) and runs it through
// RunMaintenance. Build and run failures are logged, never propagated:
// the agent emits its own EventAgentFailed, and a held lease is an
// expected "already running elsewhere" skip.
func (s *scheduler) runOne(e *schedEntry) {
	defer func() {
		s.mu.Lock()
		e.running = false
		s.mu.Unlock()
	}()

	ag, err := Build(e.sched.Agent, s.st, e.sched.Config, s.deps)
	if err != nil {
		s.log.Warn("scheduler: build agent failed", "agent", e.sched.Agent, "err", err)
		return
	}
	if _, err := s.st.RunMaintenance(s.baseCtx, ag); err != nil {
		s.log.Debug("scheduler: agent run ended with error", "agent", e.sched.Agent, "err", err)
	}
}

func (s *scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.mu.Unlock()

	s.cancel() // cancel the context handed to in-flight agents
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

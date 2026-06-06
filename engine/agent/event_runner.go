package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// EventRunner is the event-driven launch primitive (ADR-69, level 3). It
// subscribes to the bus and, per rule, runs an agent every Nth event of a
// type. It holds no resident goroutine of its own: counting happens in the
// bus delivery goroutine, and crossing a threshold spawns a short-lived
// run goroutine (joined by Stop). Overlap is prevented like the Scheduler:
// an in-flight rule is skipped, not queued, and the agent's own lease
// guards across processes.
//
// EventRunner is a primitive, not part of the client surface: thresholds
// are logic, not data (ADR-72). A host or admin that wants them builds the
// runner here, on the primitives.
type EventRunner interface {
	// On registers a rule: every `every` events of type eventType, run
	// agent kind with cfg. kind must be registered (agent.Lookup) and
	// every must be positive.
	On(eventType string, every int, kind string, cfg any) error

	// Stop unsubscribes from the bus, cancels in-flight runs and waits for
	// them, bounded by ctx. Idempotent.
	Stop(ctx context.Context) error
}

type runnerRule struct {
	eventType string
	every     int
	kind      string
	cfg       any
	count     int
	running   bool
}

type eventRunner struct {
	st   store.Store
	deps AgentDeps
	log  *slog.Logger

	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	unsub func() // bus unsubscribe

	mu      sync.Mutex
	rules   []*runnerRule
	stopped bool
}

var _ EventRunner = (*eventRunner)(nil)

// NewEventRunner builds an EventRunner over st with deps, subscribed to
// bus. With no rules it counts nothing — free until On is called.
func NewEventRunner(st store.Store, deps AgentDeps, bus event.EventBus, opts ...AgentOption) (EventRunner, error) {
	if st == nil {
		return nil, fmt.Errorf("agent.NewEventRunner: nil store")
	}
	if bus == nil {
		return nil, fmt.Errorf("agent.NewEventRunner: nil bus")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &eventRunner{
		st:      st,
		deps:    deps,
		log:     resolveAgentLogger(opts),
		baseCtx: ctx,
		cancel:  cancel,
	}
	r.unsub = bus.Subscribe(r.onEvent)
	return r, nil
}

func (r *eventRunner) On(eventType string, every int, kind string, cfg any) error {
	if eventType == "" {
		return fmt.Errorf("agent.EventRunner.On: empty event type")
	}
	if every <= 0 {
		return fmt.Errorf("agent.EventRunner.On: every must be positive for %q", kind)
	}
	if !validAgentType(kind) {
		return fmt.Errorf("%w: %q", errs.ErrInvalidAgentType, kind)
	}
	if _, ok := Lookup(kind); !ok {
		return fmt.Errorf("%w: no agent registered for %q", errs.ErrInvalidAgentType, kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return fmt.Errorf("agent.EventRunner.On: runner stopped")
	}
	r.rules = append(r.rules, &runnerRule{eventType: eventType, every: every, kind: kind, cfg: cfg})
	return nil
}

// onEvent runs in the bus delivery goroutine on every published event. It
// counts matches and, on reaching a rule's threshold, spawns a run. A
// threshold reached while that rule is still in flight resets the window
// and is skipped, not queued (ADR-69 §9.4).
func (r *eventRunner) onEvent(e event.Event) {
	var due []*runnerRule
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	for _, rule := range r.rules {
		if rule.eventType != e.Type {
			continue
		}
		rule.count++
		if rule.count < rule.every {
			continue
		}
		rule.count = 0 // reset the window whether we fire or skip
		if rule.running {
			continue
		}
		rule.running = true
		due = append(due, rule)
	}
	if len(due) > 0 {
		r.wg.Add(len(due))
	}
	r.mu.Unlock()

	for _, rule := range due {
		rule := rule
		go func() {
			defer r.wg.Done()
			r.runOne(rule)
		}()
	}
}

// runOne builds the agent fresh (agents are one-shot) and runs it through
// RunMaintenance. Build and run failures are logged, never propagated: the
// agent emits its own EventAgentFailed, and a held lease is an expected
// "already running elsewhere" skip.
func (r *eventRunner) runOne(rule *runnerRule) {
	defer func() {
		r.mu.Lock()
		rule.running = false
		r.mu.Unlock()
	}()

	ag, err := Build(rule.kind, r.st, rule.cfg, r.deps)
	if err != nil {
		r.log.Warn("event runner: build agent failed", "agent", rule.kind, "err", err)
		return
	}
	if _, err := r.st.RunMaintenance(r.baseCtx, ag); err != nil {
		r.log.Debug("event runner: agent run ended with error", "agent", rule.kind, "err", err)
	}
}

func (r *eventRunner) Stop(ctx context.Context) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	unsub := r.unsub
	r.mu.Unlock()

	if unsub != nil {
		unsub() // stop counting new events
	}
	r.cancel() // cancel the context handed to in-flight agents
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

package assembly

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/index/extension"
	"scrinium.dev/engine/store"
	"scrinium.dev/event"
	"scrinium.dev/projection"
)

// Assembly is an assembled Scrinium stack. The scrinium facade obtains
// one from Build/Load* and wraps it; release it with Close.
type Assembly interface {
	// Store is the high-level CAS store (Put/Get/Delete/Walk + admin).
	Store() store.Store

	// Extensions lists the index extensions registered on the backing
	// store index, for diagnostics (e.g. a stats page). Empty when the
	// index backend exposes none. This is the only index detail the
	// assembly surfaces: the raw StoreIndex (with its mutating
	// IndexManifest/DeletePacked) stays internal.
	//
	// Note: the assembly deliberately exposes no raw Driver either.
	// Built-in maintenance/background agents receive Driver and
	// StoreIndex directly from the assembler at construction time
	// (engine-internal); they do not reach them through this surface,
	// and neither do hosts.
	Extensions() []extension.ExtensionInfo

	// Projection is the read-side View plus the optional read/write
	// FSOps facade, bundled. Nil when the assembly was built without a
	// projection section.
	Projection() *projection.Projection

	// MountSession is the boot-unique identifier for this assembly.
	MountSession() domain.SessionID

	// Info returns assembly metadata for diagnostics (the store URI,
	// namespace, editing policy, read-only flag, fresh-create flag). A
	// cheap snapshot.
	Info() Info

	// RecoveryKit returns the recovery-kit bytes produced when this
	// assembly freshly initialised an encrypted store, and true. For a
	// store that was opened (not created) or is unencrypted it returns
	// (nil, false). The host MUST persist a returned kit out of band:
	// it is the only path back into the store if the passphrase is
	// lost. The same bytes are available later via Store().Admin()'s
	// ExportRecoveryKit.
	RecoveryKit() ([]byte, bool)

	// RunMaintenance builds the registered agent named kind (decoding
	// cfg via its factory) and runs it once through the store, returning
	// the agent's result. It is the always-available manual trigger
	// (ADR-69 level 1): no resident goroutine, no scheduler. The agent
	// receives the Driver and StoreIndex from the assembler internally,
	// so neither leaks through this surface. kind must be registered
	// (blank-import the agent package, as with drivers).
	RunMaintenance(ctx context.Context, kind string, cfg any) (*domain.AgentResult, error)

	// Subscribe registers fn to receive every store and agent event, and
	// returns a function that removes it. The bus is synchronous; fn runs
	// inline on the publishing goroutine. A handler installed at build
	// time (WithEventHandler) also sees events emitted during assembly,
	// which a post-build Subscribe cannot.
	Subscribe(fn func(event.Event)) func()

	// ScheduleEvery registers agent kind to run every interval through the
	// built-in scheduler. Requires WithStandardScheduler; without it there
	// is no scheduler to drive the schedule and it returns an error. The
	// agent is built fresh from the registry on each run.
	ScheduleEvery(kind string, every time.Duration, cfg any) error

	// ScheduleCron registers agent kind to run on a cron expression
	// through the built-in scheduler. Requires WithStandardScheduler and a
	// cron adapter (cron.Enable from scrinium.dev/engine/agent/cron);
	// without either it returns an error. The expression is parsed once
	// here; an invalid expression is reported immediately.
	ScheduleCron(kind string, expr string, cfg any) error

	// Close releases the store, index, and view, idempotently.
	Close() error
}

// Info is assembly metadata an app may surface in diagnostics (e.g. a
// stats page). The assembly itself does not act on it.
type Info struct {
	StoreURI  string
	Namespace string
	Editing   string
	ReadOnly  bool
	// Created is true when this assembly freshly initialised the store
	// (Init, or OpenOrInit that fell through to Init) rather than
	// opening an existing one.
	Created bool
}

// asm is the private concrete Assembly the assembler populates.
// Unexported: callers depend on the interface, so the assembled shape
// can grow without an API break.
type asm struct {
	store        store.Store
	index        index.StoreIndex
	proj         *projection.Projection
	mountSession domain.SessionID
	info         Info
	recoveryKit  []byte
	closeFn      func() error
	agentDeps    agent.AgentDeps  // Driver/Index/Publisher the assembler hands agents
	bus          event.EventBus   // the store+agent event channel; Subscribe taps it
	sched        agent.Scheduler  // built-in scheduler; nil unless WithStandardScheduler
	cronParser   agent.CronParser // nil unless a cron adapter was enabled
}

var _ Assembly = (*asm)(nil)

// New builds an Assembly from already-assembled components. The
// assembler is the intended caller; closeFn unwinds store/index/view in
// the correct order and must be idempotent. recoveryKit carries the
// init-time kit bytes (nil unless a fresh encrypted store was created).
func New(
	st store.Store,
	idx index.StoreIndex,
	proj *projection.Projection,
	mountSession domain.SessionID,
	info Info,
	recoveryKit []byte,
	closeFn func() error,
	agentDeps agent.AgentDeps,
	bus event.EventBus,
	sched agent.Scheduler,
	cronParser agent.CronParser,
) Assembly {
	return &asm{
		store:        st,
		index:        idx,
		proj:         proj,
		mountSession: mountSession,
		info:         info,
		recoveryKit:  recoveryKit,
		closeFn:      closeFn,
		agentDeps:    agentDeps,
		bus:          bus,
		sched:        sched,
		cronParser:   cronParser,
	}
}

func (a *asm) Store() store.Store                 { return a.store }
func (a *asm) Projection() *projection.Projection { return a.proj }
func (a *asm) MountSession() domain.SessionID     { return a.mountSession }
func (a *asm) Info() Info                         { return a.info }

// Extensions reports the index extensions when the backend implements
// extension.ExtensionLister, and nil otherwise. The raw index is held only
// internally (a.index) and never handed out.
func (a *asm) Extensions() []extension.ExtensionInfo {
	if l, ok := a.index.(extension.ExtensionLister); ok {
		return l.ListExtensions()
	}
	return nil
}

func (a *asm) RecoveryKit() ([]byte, bool) {
	if len(a.recoveryKit) == 0 {
		return nil, false
	}
	return a.recoveryKit, true
}

// RunMaintenance builds the named agent from the registry with the
// assembler-held deps and runs it once through the store. agent.Build
// validates the kind and decodes cfg via the factory; the store's
// RunMaintenance orders Validate then Run under the maintenance gate.
func (a *asm) RunMaintenance(ctx context.Context, kind string, cfg any) (*domain.AgentResult, error) {
	ag, err := agent.Build(kind, a.store, cfg, a.agentDeps)
	if err != nil {
		return nil, err
	}
	return a.store.RunMaintenance(ctx, ag)
}

func (a *asm) Subscribe(fn func(event.Event)) func() {
	return a.bus.Subscribe(fn)
}

func (a *asm) ScheduleEvery(kind string, every time.Duration, cfg any) error {
	if a.sched == nil {
		return fmt.Errorf("scrinium: standard scheduler not enabled (pass WithStandardScheduler)")
	}
	return a.sched.Add(agent.Schedule{Agent: kind, Interval: every, Config: cfg})
}

func (a *asm) ScheduleCron(kind string, expr string, cfg any) error {
	if a.sched == nil {
		return fmt.Errorf("scrinium: standard scheduler not enabled (pass WithStandardScheduler)")
	}
	if a.cronParser == nil {
		return fmt.Errorf("scrinium: cron support not enabled (use cron.Enable from scrinium.dev/engine/agent/cron)")
	}
	next, err := a.cronParser(expr)
	if err != nil {
		return fmt.Errorf("scrinium: cron %q: %w", expr, err)
	}
	return a.sched.Add(agent.Schedule{Agent: kind, Next: next, Config: cfg})
}

func (a *asm) Close() error {
	if a.closeFn == nil {
		return nil
	}
	return a.closeFn()
}

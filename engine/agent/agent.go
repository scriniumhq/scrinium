package agent

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// State is the lifecycle state of an agent reported by Status.
type State uint8

const (
	// StateIdle — Run has not been started yet, or it finished
	// cleanly.
	StateIdle State = iota

	// StateRunning — Run is active.
	StateRunning

	// StateFaulted — Run finished with an error; Status returns a
	// non-nil error explaining the cause.
	StateFaulted
)

// baseState is the shared lifecycle state of an agent: a mutex-guarded
// current State plus the last fatal error. Embed it to get Status and
// setState for free.
type baseState struct {
	mu    sync.Mutex
	state State
	err   error
	log   *slog.Logger
}

// Status returns the current state and the last error. Safe for
// concurrent calls with the agent's Run.
func (b *baseState) Status() (State, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.err
}

// setState records a new state and error under the lock.
func (b *baseState) setState(s State, err error) {
	b.mu.Lock()
	b.state, b.err = s, err
	b.mu.Unlock()
}

// logger returns the agent's diagnostic logger, never nil. Following
// ADR-60 the default is silence: an agent built without a logger uses
// slog.DiscardHandler and pays nothing (Enabled == false). Logs explain;
// they never replace a returned error (no log-and-return).
func (b *baseState) logger() *slog.Logger {
	if b.log == nil {
		return discardLogger
	}
	return b.log
}

// discardLogger is the shared no-op logger (ADR-60: default is silence,
// no slog.Default() reach-through).
var discardLogger = slog.New(slog.DiscardHandler)

// AgentOption tunes an agent at construction. The variadic form keeps
// existing constructor call sites source-compatible; the assembler
// passes WithAgentLogger(deps.Logger) through Factory.Build.
type AgentOption func(*agentOptions)

type agentOptions struct{ logger *slog.Logger }

// WithAgentLogger sets the agent's diagnostic logger (ADR-60).
func WithAgentLogger(l *slog.Logger) AgentOption {
	return func(o *agentOptions) { o.logger = l }
}

// resolveAgentLogger folds options and defaults a missing logger to
// silence.
func resolveAgentLogger(opts []AgentOption) *slog.Logger {
	var o agentOptions
	for _, fn := range opts {
		fn(&o)
	}
	if o.logger == nil {
		return discardLogger
	}
	return o.logger
}

// Agent is the single lifecycle contract of a Scrinium agent (ADR-68).
// An agent is a one-shot procedure over a Store — Validate then Run —
// initiated outside the operation path. There is no Background versus
// Maintenance split: periodic invocation is the scheduler's job, not a
// property of the agent. Agents keep no resident in-memory state;
// progress lives in the Store, so an interrupted Run resumes the
// remainder on the next call.
//
// A public SPI: the host application can implement custom agents and
// register them through Register under their own namespaced AgentType.
type Agent interface {
	// AgentType is the short identifier of the implementation
	// ("gc", "scrub", ...), used to tag the agent's events and as
	// the registry key.
	AgentType() string

	// Validate checks the preconditions of the operation: Store
	// mode, required parameters and dependencies, absence of a
	// competing lease. It makes no irreversible changes.
	Validate(ctx context.Context) error

	// Run performs the operation and blocks until it completes. It
	// returns the business result (*domain.AgentResult) and an
	// infrastructure error. Idempotent: an interrupted Run picks up
	// the remainder on the next call (progress lives in the Store).
	Run(ctx context.Context) (*domain.AgentResult, error)

	// Status returns the current state and the last error, race-free
	// with Run.
	Status() (State, error)
}

// AgentDeps are the dependencies an assembler passes to Factory.Build.
// Driver and Index are the same objects held inside the Store; they
// arrive here from the assembler rather than being pulled out of the
// Store, so the facade is not opened up. The struct extends as new
// dependencies appear.
type AgentDeps struct {
	Publisher event.Publisher  // event channel
	Driver    driver.Driver    // the same object the Store holds
	Index     index.StoreIndex // the same object the Store holds
	HostID    string           // per-process UUID v4, for lease takeover events
	StoreID   string           // tags the agent's events; from the descriptor
	Logger    *slog.Logger     // diagnostics; nil = silence (ADR-60)
}

// Factory builds an agent of one kind from declarative config and the
// assembler-provided deps. Built-in factories register through
// Register in an init().
type Factory interface {
	// Name is the AgentType this factory builds.
	Name() string

	// Build constructs the agent. cfg is the kind-specific config
	// (for example, GCConfig); a nil or zero cfg means defaults.
	Build(st store.Store, cfg any, deps AgentDeps) (Agent, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// agentTypePattern matches a short built-in name ("gc") or a
// namespaced extension name ("acme.replicator"). Lowercase only.
var agentTypePattern = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)?$`)

// validAgentType reports whether name is a well-formed AgentType.
func validAgentType(name string) bool {
	return agentTypePattern.MatchString(name)
}

// Register installs a Factory under its Name. Called from an init().
// It panics on a nil factory, an invalid name, or a duplicate — all
// programmer errors at wiring time.
func Register(f Factory) {
	if f == nil {
		panic("agent.Register: nil factory")
	}
	name := f.Name()
	if !validAgentType(name) {
		panic(fmt.Sprintf("agent.Register: invalid agent type %q", name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("agent.Register: duplicate agent type %q", name))
	}
	registry[name] = f
}

// Lookup returns the Factory registered for name.
func Lookup(name string) (Factory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	return f, ok
}

// Build resolves the Factory for kind and constructs the agent. An
// ill-formed name or an unregistered kind returns
// errs.ErrInvalidAgentType — a clear error, not a panic.
func Build(kind string, st store.Store, cfg any, deps AgentDeps) (Agent, error) {
	if !validAgentType(kind) {
		return nil, fmt.Errorf("%w: %q", errs.ErrInvalidAgentType, kind)
	}
	f, ok := Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("%w: no agent registered for %q", errs.ErrInvalidAgentType, kind)
	}
	return f.Build(st, cfg, deps)
}

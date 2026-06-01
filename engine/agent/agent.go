package agent

import (
	"context"
	"sync"
)

// baseState is the shared lifecycle state of the interval-loop agents
// (gc, scrub, snapshot): a mutex-guarded current State plus the last
// fatal error. Embed it to get Status and setState for free — all
// three had byte-identical copies before.
type baseState struct {
	mu    sync.Mutex
	state State
	err   error
}

// Status returns the current state and the last error. Safe for
// concurrent calls with the agent's Run loop.
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

// State is the state of a background agent reported by Status.
type State uint8

const (
	// StateIdle — Run has not been started yet, or it has finished
	// cleanly.
	StateIdle State = iota

	// StateRunning — Run is active; the current unit of work is in
	// flight.
	StateRunning

	// StatePaused — reserved. Not used by any built-in agent in
	// v1; the slot is held for the future "auto-pause under
	// pressure" backlog item.
	StatePaused

	// StateFaulted — Run finished with an error; Status returns a
	// non-nil error explaining the cause.
	StateFaulted
)

// BackgroundAgent is the base lifecycle contract of a background
// agent. A public SPI: the host application can implement custom
// agents for bespoke validators, metric exporters, business-specific
// integrations.
//
// Conventions for AgentType (used in EventAgent* payloads):
//   - Built-in agents: short names without a prefix ("gc",
//     "scrub", "snapshot", "ingester", "ejector", "sync").
//   - User agents: <namespace>.<n> ("acme.quota_monitor").
//
// The event.Event.Type prefixes "store.", "agent.", "index.", and
// "projection." are reserved. User agents must emit their own events
// under their own namespace.
type BackgroundAgent interface {
	// Run starts the main loop. Blocks until ctx is cancelled or a
	// fatal error occurs. Returns nil on a graceful shutdown via
	// ctx, an error on a fatal failure. The results of individual
	// units of work are published through EventAgentCycle.
	Run(ctx context.Context) error

	// Status returns the current state and the last error. Must be
	// safe for concurrent calls with Run.
	Status() (State, error)
}

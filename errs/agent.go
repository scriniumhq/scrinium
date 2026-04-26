package errs

import "errors"

// Background-agent control plane: agent lifecycle, command-style
// methods, queue back-pressure. Used by the agent package and its
// built-ins (Ingester, GC, Scrub, Snapshot, Sync, Ejector).

// ErrAgentNotRunning — command-style methods (ForceCommit, Eject,
// Trigger, TakeSnapshot, ...) called on an agent whose Run is not
// active.
var ErrAgentNotRunning = errors.New("scrinium: agent not running")

// ErrAgentAlreadyRunning — Run was called again on an already
// running agent.
var ErrAgentAlreadyRunning = errors.New("scrinium: agent already running")

// ErrEjectorQueueFull — Ejector.Eject called while the task queue
// is already full.
var ErrEjectorQueueFull = errors.New("scrinium: ejector queue full")

// ErrIngesterNoState — NewIngester invoked with Mode: Watch but no
// StateFile configured.
var ErrIngesterNoState = errors.New("scrinium: ingester watch mode requires StateFile")

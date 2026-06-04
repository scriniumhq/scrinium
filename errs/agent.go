package errs

import "errors"

// Background-agent control plane: agent lifecycle, command-style
// methods, queue back-pressure. Used by the agent package and its
// built-ins (Ingester, GC, Scrub, Snapshot, Sync, Ejector).

// ErrAgentNotRunning — command-style methods (ForceCommit, Eject,
// Trigger, TakeCheckpoint, ...) called on an agent whose Run is not
// active.
var ErrAgentNotRunning = errors.New("scrinium: agent not running")

// ErrAgentAlreadyRunning — Run was called again on an already
// running agent.
var ErrAgentAlreadyRunning = errors.New("scrinium: agent already running")

// ErrInvalidAgentType — an AgentType is ill-formed (not lowercase
// [a-z0-9-], optionally one namespaced segment) or no agent is
// registered for it. Returned by the agent registry.
var ErrInvalidAgentType = errors.New("scrinium: invalid agent type")

// ErrInvalidRange — EjectFragment was called with an out-of-bounds or
// inverted byte range (need 0 <= start < end <= OriginalSize).
var ErrInvalidRange = errors.New("scrinium: invalid fragment range")

// ErrFragmentTooLarge — the requested fragment exceeds the Ejector's
// MaxFragmentBytes guardrail.
var ErrFragmentTooLarge = errors.New("scrinium: fragment too large")

// ErrEjectorTempDirFull — the Ejector's TempDir ran out of quota or
// disk space while materialising an artifact.
var ErrEjectorTempDirFull = errors.New("scrinium: ejector temp dir full")

// ErrEjectorClosed — Eject was called on an Ejector that has been
// closed; new materialisations are rejected.
var ErrEjectorClosed = errors.New("scrinium: ejector closed")

// ErrEjectorQueueFull — Ejector.Eject called while the task queue
// is already full.
var ErrEjectorQueueFull = errors.New("scrinium: ejector queue full")

// ErrIngesterNoState — NewIngester invoked with Mode: Watch but no
// StateFile configured.
var ErrIngesterNoState = errors.New("scrinium: ingester watch mode requires StateFile")

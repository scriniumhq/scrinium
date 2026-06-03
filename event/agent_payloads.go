package event

import (
	"time"

	"scrinium.dev/domain"
)

// agent_payloads.go — the "agent." event-type constants and their
// payload structs. Pure data (domain types and stdlib only), so the
// identity of every agent lifecycle event lives in this leaf package
// rather than in the emitting agent. The host filters by AgentType in
// the payload — the same constant set is reused for "gc", "scrub", and
// any custom agent the host has registered. User agents emit their own
// events under their own namespace ("acme.quota_monitor.tripped").
const (
	// EventAgentStarted — Run entered its work. Emitted once per Run.
	EventAgentStarted = "agent.started"

	// EventAgentProgress — periodic progress snapshot. The emission
	// rate is agent-specific.
	EventAgentProgress = "agent.progress"

	// EventAgentCycle — one unit of work completed while the agent
	// continues (a full GC pass, a single Scrub batch). Payload:
	// domain.AgentResult. The same payload shape is reused by
	// EventAgentCompleted; the difference is semantic — Cycle means
	// "one unit done, agent continues", Completed means "Run returned".
	EventAgentCycle = "agent.cycle"

	// EventAgentCompleted — Run returned cleanly. Payload:
	// domain.AgentResult.
	EventAgentCompleted = "agent.completed"

	// EventAgentFailed — Run returned with a fatal error.
	EventAgentFailed = "agent.failed"

	// EventAgentStopped — graceful stop via context cancellation.
	EventAgentStopped = "agent.stopped"

	// EventAgentCancelled — Run aborted before completion.
	EventAgentCancelled = "agent.cancelled"

	// EventAgentStaleLease — the agent took over a lease whose owner
	// stopped renewing. Payload: LeaseTakeoverPayload — the same
	// struct EventStaleLeaseTakeover uses, declared once here because
	// the stale-lease concept is shared with the Store-level lease
	// takeover and the two events stay decoder-compatible.
	EventAgentStaleLease = "agent.stale_lease"
)

// AgentStartedPayload is the payload of EventAgentStarted.
type AgentStartedPayload struct {
	AgentType string
	StoreID   string
	StartedAt time.Time
}

// AgentProgressPayload is the payload of EventAgentProgress. Total is
// 0 when the total amount of work is unknown up front.
type AgentProgressPayload struct {
	AgentType   string
	StoreID     string
	Processed   int64
	Total       int64
	CurrentItem string
}

// AgentFailedPayload is the payload of EventAgentFailed.
type AgentFailedPayload struct {
	AgentType string
	StoreID   string
	Err       error
}

// --- Ejector facts (agent "ejector", Reference 06 Agents/07) ---
//
// The Ejector materialises artifacts onto the host filesystem as plain
// POSIX files on demand. These facts live under the "agent." namespace
// because the Ejector is an agent; they are distinct from the run
// lifecycle above (EventAgentFailed reports a failed Run, EventEjectFailed
// reports a single failed materialisation).
const (
	// EventArtifactEjected — one artifact was materialised to a host
	// path. Method is "clone" (CoW reflink) or "copy".
	EventArtifactEjected = "agent.artifact_ejected"

	// EventEjectFailed — a single Eject failed.
	EventEjectFailed = "agent.eject_failed"

	// EventEjectorCleanup — a Run swept expired materialisations.
	EventEjectorCleanup = "agent.eject_cleanup"
)

// ArtifactEjectedPayload is the payload of EventArtifactEjected.
type ArtifactEjectedPayload struct {
	AgentType   string
	StoreID     string
	ArtifactID  domain.ArtifactID
	ContentHash string // name key of the materialised file
	Path        string
	Method      string // "clone" or "copy"
	Start       int64  // fragment start offset (0 for whole)
	Length      int64  // materialised byte length
}

// EjectFailedPayload is the payload of EventEjectFailed.
type EjectFailedPayload struct {
	AgentType  string
	StoreID    string
	ArtifactID domain.ArtifactID
	Err        error
}

// EjectorCleanupPayload is the payload of EventEjectorCleanup. Evicted
// is the number of materialisations removed in the sweep.
type EjectorCleanupPayload struct {
	AgentType string
	StoreID   string
	Evicted   int64
	Reason    string // "idle" | "lifetime" | "pressure" | "close"
}

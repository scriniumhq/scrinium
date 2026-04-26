package agent

import (
	"time"
)

// Agent lifecycle events. Emitted by every BackgroundAgent
// (built-in or user-defined) at well-defined points in its
// run loop.
//
// The host application filters by AgentType in the payload — the
// same constant set is reused for "gc", "scrub", "ingester", and
// any custom agent the host has registered.
//
// All four event-type prefixes ("core.", "agent.", "curator.",
// "index.") are reserved per docs/2. Internals/01 §1.7. User
// agents must emit their own events under their own namespace
// (e.g. "acme.quota_monitor.threshold_exceeded").
const (
	// EventAgentStarted — Run entered its main loop. Emitted once
	// per Run call.
	EventAgentStarted = "agent.started"

	// EventAgentProgress — periodic progress snapshot. Emission
	// rate is agent-specific; defaults are documented at each
	// concrete agent.
	EventAgentProgress = "agent.progress"

	// EventAgentCycle — one unit of work completed (a full GC
	// pass, a single Scrub batch, one Ingester flush). Carries no
	// payload — operators read counts and timings from
	// AgentResult, queried separately.
	EventAgentCycle = "agent.cycle"

	// EventAgentCompleted — Run returned cleanly (only meaningful
	// for one-shot agents like Snapshot or RebuildIndex).
	// Background agents in steady state emit Cycle, not Completed.
	EventAgentCompleted = "agent.completed"

	// EventAgentFailed — Run returned with a fatal error.
	EventAgentFailed = "agent.failed"

	// EventAgentStopped — graceful shutdown via context
	// cancellation.
	EventAgentStopped = "agent.stopped"

	// EventAgentCancelled — Run aborted before completion (host
	// requested cancellation, lease lost, etc.).
	EventAgentCancelled = "agent.cancelled"

	// EventAgentStaleLease — agent took over a lease whose owner
	// stopped renewing. Payload: core.LeaseTakeoverPayload (same
	// shape as core.EventStaleLeaseTakeover; see core/events.go).
	// The struct lives in core because the stale-lease concept is
	// shared with the core-level Store lease takeover; declaring
	// it once and reusing it keeps the two events decoder-
	// compatible.
	EventAgentStaleLease = "agent.stale_lease"
)

// --- Payload structs ---

// AgentStartedPayload is the payload of EventAgentStarted.
type AgentStartedPayload struct {
	AgentType string
	StoreID   string
	StartedAt time.Time
}

// AgentProgressPayload is the payload of EventAgentProgress. Total
// is 0 when the total amount of work is unknown up front (for
// example, in a continuous loop).
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

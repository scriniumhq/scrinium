package core

import "time"

// Engine event-type constants. Used as the value of
// event.Event.Type. Curator-level events (curator.*), index events
// (index.*), and agent events (agent.*) live in their own packages.
const (
	EventManifestSaved         = "core.manifest_saved"
	EventArtifactDeleted       = "core.artifact_deleted"
	EventBlobPhysicallyDeleted = "core.blob_physically_deleted"
	EventPackCompacted         = "core.pack_compacted"
	EventCapacityWarning       = "core.capacity_warning"
	EventScrubFailed           = "core.scrub_failed"
	EventStoreDegraded         = "core.store_degraded"
	EventKEKRotated            = "core.kek_rotated"
	EventStaleLeaseTakeover    = "core.stale_lease_takeover"

	// Agent lifecycle events. The host application filters by
	// AgentType in the payload.
	EventAgentStarted    = "agent.started"
	EventAgentProgress   = "agent.progress"
	EventAgentCycle      = "agent.cycle"
	EventAgentCompleted  = "agent.completed"
	EventAgentFailed     = "agent.failed"
	EventAgentStopped    = "agent.stopped"
	EventAgentCancelled  = "agent.cancelled"
	EventAgentStaleLease = "agent.stale_lease"
)

// --- Payload structs ---

// ManifestSavedPayload is the payload of EventManifestSaved.
// IsTransit is true when the file was placed into
// HostStorage.system.transit and has not yet been drained to a
// Target. After Drain (at the Curator level) EventDrainCompleted
// is emitted.
type ManifestSavedPayload struct {
	Manifest  Manifest
	IsTransit bool
}

// ArtifactDeletedPayload is the payload of EventArtifactDeleted.
// Emitted only when the logical deletion actually happens. If the
// deletion is rejected by retention or policy, the event is not
// emitted.
type ArtifactDeletedPayload struct {
	ArtifactID ArtifactID
}

// BlobPhysicallyDeletedPayload is the payload of
// EventBlobPhysicallyDeleted. Emitted by the GC Agent when the
// Sweep phase completes (physical removal of the file after the
// Grace Period).
type BlobPhysicallyDeletedPayload struct {
	BlobRef string
}

// PackCompactedPayload is the payload of EventPackCompacted.
// Emitted on a successful compaction of a partially dead pack
// volume.
type PackCompactedPayload struct {
	OldPackRef  string
	NewPackRef  string
	LiveEntries int
	DeadEntries int
	OldSize     int64
	NewSize     int64
	FreedBytes  int64
	Duration    time.Duration
}

// CapacityWarningPayload is the payload of EventCapacityWarning.
// UsedPct is the share of used space (0..100).
type CapacityWarningPayload struct {
	UsedPct float64
}

// ScrubFailedPayload is the payload of EventScrubFailed. Emitted
// by the Scrub Agent or by Store.Verify on a hash divergence.
type ScrubFailedPayload struct {
	ArtifactID ArtifactID
	Err        error
}

// StoreDegradedPayload is the payload of EventStoreDegraded.
// Emitted on a transition into StateDegraded (descriptor-replica
// divergence).
type StoreDegradedPayload struct {
	Reason string
}

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

// LeaseTakeoverPayload is the payload of
// EventStaleLeaseTakeover and EventAgentStaleLease.
type LeaseTakeoverPayload struct {
	LeaseKey       string
	PreviousHolder string
	ExpiredAt      time.Time
	TakenBy        string
}

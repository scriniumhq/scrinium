package core

import (
	"time"

	"github.com/rkurbatov/scrinium/domain"
)

// Engine event-type constants. Used as the value of
// event.Event.Type.
//
// Other reserved namespaces and their owners:
//   - "agent.*"   — agent/events.go
//   - "curator.*" — curator/curator.go
//   - "index.*"   — index/events.go
//
// User code must emit events under its own namespace. The reserved
// prefixes are enforced by convention; see docs/2. Internals/01 §1.7.
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
	EventOrphanScanCompleted   = "core.orphan_scan_completed"
)

// --- Payload structs ---

// ManifestSavedPayload is the payload of EventManifestSaved.
// IsTransit is true when the file was placed into
// HostStorage.system.transit and has not yet been drained to a
// Target. After Drain (at the Curator level) EventDrainCompleted
// is emitted.
type ManifestSavedPayload struct {
	Manifest  domain.Manifest
	IsTransit bool
}

// ArtifactDeletedPayload is the payload of EventArtifactDeleted.
// Emitted only when the logical deletion actually happens. If the
// deletion is rejected by retention or policy, the event is not
// emitted.
type ArtifactDeletedPayload struct {
	ArtifactID domain.ArtifactID
}

// OrphanScanCompletedPayload is the payload of
// EventOrphanScanCompleted. Emitted by the bootstrap recovery
// after every transition into Unlocked, summarising what the scan
// found and removed. Counts are physical files removed; non-fatal
// I/O errors during the scan (per-file Remove failures, individual
// path-parse glitches) are aggregated into NonFatalErrors. The
// scan never refuses to open a Store — operators read the count
// here and dig into engine logs for details.
type OrphanScanCompletedPayload struct {
	StagingRemoved   int
	BlobsRemoved     int
	ManifestsRemoved int
	NonFatalErrors   int
	Duration         time.Duration
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
	ArtifactID domain.ArtifactID
	Err        error
}

// StoreDegradedPayload is the payload of EventStoreDegraded.
// Emitted on a transition into StateDegraded (descriptor-replica
// divergence).
type StoreDegradedPayload struct {
	Reason string
}

// LeaseTakeoverPayload is the payload of two events:
// core.EventStaleLeaseTakeover (Store-level lease — Open under
// stale location.lock) and agent.EventAgentStaleLease (an agent
// took over from a previous holder that stopped renewing). The
// stale-lease concept is layer-agnostic, so a single struct
// describes both. Lives in core because core was the first
// emitter; agent imports core for unrelated reasons already, so
// no new dependency is introduced.
type LeaseTakeoverPayload struct {
	LeaseKey       string
	PreviousHolder string
	ExpiredAt      time.Time
	TakenBy        string
}

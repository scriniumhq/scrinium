package curator

import (
	"context"
	"errors"
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// Curator is the L3 facade. It implements core.DataStore, adds
// access to registered Stores, transit management, and graceful
// shutdown.
type Curator interface {
	core.DataStore

	// MultistoreIndex returns the global index when one has been
	// registered. It is usually nil with a single Target Store.
	MultistoreIndex() MultistoreIndex

	// Store returns a registered Target Store by ID with the full
	// core.Store interface, including administrative methods.
	// Returns ErrStoreNotRegistered for an unknown ID.
	Store(id string) (core.Store, error)

	// Close stops every background process in order: Flush bundler →
	// Drain HostStorage → stop agents → wait for active Get calls.
	Close(ctx context.Context) error

	// Stats returns a snapshot of the local transit-buffer state.
	Stats(ctx context.Context) (HostStorageStats, error)
}

// HostStorageStats is the current physical state of the transit
// buffer. The values are a snapshot at the moment of the request.
type HostStorageStats struct {
	TransitBytes    int64 // bytes in transit (excluding quarantine)
	TransitFiles    int   // number of files awaiting Drain
	QuarantineBytes int64 // bytes in system.transit/quarantine/
	QuarantineFiles int   // number of files in quarantine
	MaxTransitBytes int64 // hard limit from configuration
}

// --- Sentinel errors ---

// ErrCuratorClosed — operation on a closed Curator.
var ErrCuratorClosed = errors.New("curator: closed")

// ErrStoreNotRegistered — Store(id) with an unknown id.
var ErrStoreNotRegistered = errors.New("curator: store not registered")

// ErrHostStorageFull — HostStorage hit its hard limit while
// OnHostStorageFull: Reject was in effect.
var ErrHostStorageFull = errors.New("curator: host storage full: soft eviction insufficient")

// ErrDrainNoTarget — at Drain time the Router returned an empty
// target list. The follow-up behaviour is determined by
// OnDrainNoTarget.
var ErrDrainNoTarget = errors.New("curator: drain: router returned no targets")

// --- Event constants ---

const (
	EventHostStoragePressure = "curator.host_storage_pressure"
	EventHostStorageFull     = "curator.host_storage_full"
	EventBackupUnavailable   = "curator.backup_unavailable"
	EventReplicationLag      = "curator.replication_lag"
	EventStoreUnreachable    = "curator.store_unreachable"
	EventDrainCompleted      = "curator.drain_completed"
	EventDrainNoTarget       = "curator.drain_no_target"
	EventDrainQuarantined    = "curator.drain_quarantined"
	EventDrainRequeued       = "curator.drain_requeued"
	EventDrainRetry          = "curator.drain_retry"
	EventCuratorWarning      = "curator.warning"
)

// --- Event payloads ---

type HostStoragePressurePayload struct {
	UsedPct        float64
	AvailableBytes int64
}

type HostStorageFullPayload struct {
	UsedBytes int64
	MaxBytes  int64
}

type BackupUnavailablePayload struct {
	StoreID    string
	TargetID   string
	ArtifactID core.ArtifactID
}

type ReplicationLagPayload struct {
	StoreID  string
	LagCount int
	LagBytes int64
}

type StoreUnreachablePayload struct {
	StoreID string
	Reason  string
}

type DrainCompletedPayload struct {
	ArtifactID core.ArtifactID
	StoreID    string
	BlobRef    string
}

type DrainNoTargetPayload struct {
	ArtifactID core.ArtifactID
	BlobRef    string
	Namespace  string
	Reason     string
}

type DrainQuarantinedPayload struct {
	ArtifactID core.ArtifactID
	BlobRef    string
	Namespace  string
	Reason     string
}

type DrainRequeuedPayload struct {
	ArtifactID    core.ArtifactID
	BlobRef       string
	Namespace     string
	QuarantinedAt time.Time
}

type DrainRetryPayload struct {
	ArtifactID   core.ArtifactID
	BlobRef      string
	TotalTargets int
	FailedCount  int
	FirstError   string
}

// CuratorWarningPayload describes a non-blocking violation.
// Rule is a machine-readable rule identifier; Message is a
// human-readable description.
type CuratorWarningPayload struct {
	Rule    string
	Message string
}

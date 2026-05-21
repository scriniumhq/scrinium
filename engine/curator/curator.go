package curator

import (
	"context"
	"time"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/wrapper/host"
	"scrinium.dev/engine/wrapper/multistore"
)

// Curator is the L3 facade. It implements core.DataStore, adds
// access to registered Stores, transit management, and graceful
// shutdown.
type Curator interface {
	coreapi.DataStore

	// MultistoreIndex returns the global index when one has been
	// registered. It is usually nil with a single Target Store.
	MultistoreIndex() multistore.MultistoreIndex

	// Store returns a registered Target Store by ID with the full
	// core.Store interface, including administrative methods.
	// Returns ErrStoreNotRegistered for an unknown ID.
	Store(id string) (coreapi.Store, error)

	// Close stops every background process in order: Flush bundler →
	// Drain HostStorage → stop agents → wait for active Get calls.
	Close(ctx context.Context) error

	// Stats returns a snapshot of the local transit-buffer state.
	Stats(ctx context.Context) (host.HostStorageStats, error)
}

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
	ArtifactID domain.ArtifactID
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
	ArtifactID domain.ArtifactID
	StoreID    string
	BlobRef    string
}

type DrainNoTargetPayload struct {
	ArtifactID domain.ArtifactID
	BlobRef    string
	Namespace  string
	Reason     string
}

type DrainQuarantinedPayload struct {
	ArtifactID domain.ArtifactID
	BlobRef    string
	Namespace  string
	Reason     string
}

type DrainRequeuedPayload struct {
	ArtifactID    domain.ArtifactID
	BlobRef       string
	Namespace     string
	QuarantinedAt time.Time
}

type DrainRetryPayload struct {
	ArtifactID   domain.ArtifactID
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

package core

// StoreState is the state of a Store. A finite state machine with
// five states; transitions are initiated by the engine during
// bootstrap/recovery or by explicit client calls (Unlock,
// SetMaintenanceMode).
type StoreState string

const (
	// StateBootstrapping — initialisation, Orphan Scan, or descriptor
	// consensus is in progress. The API is blocked.
	StateBootstrapping StoreState = "Bootstrapping"

	// StateLocked — the descriptor has been read; the DEK has not
	// yet been decrypted. Awaits Unlock with a passphrase.
	StateLocked StoreState = "Locked"

	// StateUnlocked — normal working state. All operations are
	// available unless restricted by MaintenanceMode or configuration
	// policy.
	StateUnlocked StoreState = "Unlocked"

	// StateDegraded — a divergence in descriptor consensus has been
	// detected. The API is available; Auto-Heal will reconcile the
	// divergence and transition the Store to Unlocked.
	StateDegraded StoreState = "Degraded"

	// StateCorrupted — a critical initialisation failure (every
	// descriptor replica is corrupted, or the StoreIndex is
	// corrupted). The API is blocked. Recovery is performed through
	// an explicit RebuildIndexAgent.
	StateCorrupted StoreState = "Corrupted"
)

// MaintenanceMode is a maintenance regime orthogonal to StoreState.
// It imposes additional restrictions on top of the regular Store
// operation.
type MaintenanceMode uint8

const (
	// MaintenanceModeNone — normal operation, no extra restrictions.
	MaintenanceModeNone MaintenanceMode = iota

	// MaintenanceModeReadOnly — writes are blocked, reads are
	// available. In-flight writes are allowed to finish; on timeout
	// they are aborted.
	MaintenanceModeReadOnly

	// MaintenanceModeOffline — the Store is fully unavailable. Only
	// State, Capabilities, and the inverse SetMaintenanceMode call
	// remain accessible.
	MaintenanceModeOffline
)

// Workspace is the physical placement of a file in the StoreIndex
// schema. The numeric values are part of the schema format —
// reordering them requires a migration via MigrateIndexAgent.
type Workspace uint8

const (
	// WorkspaceLocation — the final storage location (target
	// Location: disk, S3 bucket).
	WorkspaceLocation Workspace = 0

	// WorkspaceHost — a transit buffer on a fast local disk
	// (HostStorage, system.transit).
	WorkspaceHost Workspace = 1
)

// PhysicalAddress is the physical address of a blob inside a Store.
// Returned from StoreIndex.Resolve. The fields PackRef, Offset, and
// Size are filled for blobs inside .pack volumes; for standalone
// files Path is used.
type PhysicalAddress struct {
	Workspace Workspace
	Path      string
	PackRef   string
	Offset    int64
	Size      int64
}

// StorageInfo holds aggregated storage metrics. Returned by
// Store.Capacity. A value of -1 in a byte field is the sentinel
// "the driver cannot determine this" (for example, S3 without a
// quota).
type StorageInfo struct {
	TotalBytes     int64 // -1 if unavailable
	UsedBytes      int64
	AvailableBytes int64 // -1 if unavailable
	ArtifactCount  int64
	BlobCount      int64
}

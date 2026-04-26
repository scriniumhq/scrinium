package domain

// StoreState is the state of a Store.
type StoreState string

const (
	StateBootstrapping StoreState = "Bootstrapping"
	StateLocked        StoreState = "Locked"
	StateUnlocked      StoreState = "Unlocked"
	StateDegraded      StoreState = "Degraded"
	StateCorrupted     StoreState = "Corrupted"
)

// MaintenanceMode is a maintenance regime orthogonal to StoreState.
type MaintenanceMode uint8

const (
	MaintenanceModeNone MaintenanceMode = iota
	MaintenanceModeReadOnly
	MaintenanceModeOffline
)

// Workspace is the physical placement of a file in the StoreIndex
// schema. The numeric values are part of the schema format.
type Workspace uint8

const (
	WorkspaceLocation Workspace = 0
	WorkspaceHost     Workspace = 1
)

// PhysicalAddress is the physical address of a blob inside a Store.
type PhysicalAddress struct {
	Workspace Workspace
	Path      string
	PackRef   string
	Offset    int64
	Size      int64
}

// StorageInfo holds aggregated storage metrics. -1 means unavailable.
type StorageInfo struct {
	TotalBytes     int64
	UsedBytes      int64
	AvailableBytes int64
	ArtifactCount  int64
	BlobCount      int64
}

// BlobExistStatus is the result of ExistsByHash.
type BlobExistStatus uint8

const (
	BlobNotFound    BlobExistStatus = 0
	BlobExists      BlobExistStatus = 1
	BlobIsTombstone BlobExistStatus = 2
)

// PackedBlobInfo is the data needed for a range read of a single
// packed blob from a .pack volume.
type PackedBlobInfo struct {
	PackBlobRef    string
	ManifestOffset int64
	ManifestSize   int64
	BlobOffset     int64
	BlobSize       int64
	PipelineParams []byte
}

// PackedEntry describes one entry inside a .pack volume.
type PackedEntry struct {
	ArtifactID     ArtifactID
	BlobRef        string
	ManifestOffset int64
	ManifestSize   int64
	BlobOffset     int64
	BlobSize       int64

	ContentHash    ContentHash
	Namespace      string
	SessionID      string
	PipelineParams []byte
}

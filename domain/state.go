package domain

// StoreState is the state of a Store.
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
type MaintenanceMode uint8

const (
	MaintenanceModeNone MaintenanceMode = iota
	MaintenanceModeReadOnly
	MaintenanceModeOffline
)

// PhysicalAddress is the physical address of a blob inside a Store.
type PhysicalAddress struct {
	Path    string
	PackRef string
	Offset  int64
	Size    int64
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

	ContentHash ContentHash
	// CryptoIdentity carries the packed blob's crypto-identity
	// (ADR-58) so the dedup key survives packing. The bundler
	// transfers it from the source blob — it packs the finished
	// ciphertext bytes as-is and never re-encrypts, so the identity
	// is unchanged. Empty for a Plain packed blob. Persisted in
	// packed_blobs.crypto_identity; pack-layer dedup (M4/S4) reads it.
	CryptoIdentity CryptoIdentity
	Namespace      string
	SessionID      SessionID
	PipelineParams []byte
}

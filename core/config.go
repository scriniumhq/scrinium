package core

import "time"

// PathTopology is the topology of paths inside a Location. Immutable
// parameter.
type PathTopology string

const (
	// PathTopologyFlat — flat list (blobs/<hash>). Default for S3.
	PathTopologyFlat PathTopology = "Flat"

	// PathTopologySharded — sharded layout (blobs/ha/sh/<hash>).
	// Default for local POSIX filesystems. Reduces load on directories.
	PathTopologySharded PathTopology = "Sharded"

	// PathTopologyNative — original paths. Allowed only with
	// BlobStorage: ExternalRef (read-only indexing).
	PathTopologyNative PathTopology = "Native"
)

// ManifestStorage controls where the manifest file lives. Immutable
// parameter.
type ManifestStorage string

const (
	// ManifestStorageRemote — in the Location workspace.
	ManifestStorageRemote ManifestStorage = "Remote"

	// ManifestStorageLocal — only in HostStorage.system.manifests.
	// Suitable for read-only media or very slow Locations.
	ManifestStorageLocal ManifestStorage = "Local"

	// ManifestStorageReplicated — in both places. Location wins on
	// divergence.
	ManifestStorageReplicated ManifestStorage = "Replicated"
)

// BlobStorage is the blob placement strategy. A mutable parameter
// (with the caveat that the Target ↔ ExternalRef transition affects
// the client call contract).
type BlobStorage string

const (
	// BlobStorageTarget — Scrinium fully owns the blob.
	BlobStorageTarget BlobStorage = "Target"

	// BlobStorageInlineFallback — for micro-files (< InlineBlobLimit)
	// the blob is embedded into the manifest. Deduplication is
	// forcibly disabled.
	BlobStorageInlineFallback BlobStorage = "InlineFallback"

	// BlobStorageExternalRef — Scrinium does not write the blob; it
	// records a manifest with a reference to the external URI.
	// The Pipeline is not applied.
	BlobStorageExternalRef BlobStorage = "ExternalRef"
)

// ManifestEncoding is the on-disk serialisation format of a manifest.
// Immutable parameter.
type ManifestEncoding string

const (
	ManifestEncodingJSON   ManifestEncoding = "JSON"
	ManifestEncodingBinary ManifestEncoding = "Binary"
)

// ManifestCrypto controls manifest protection. Immutable parameter.
type ManifestCrypto string

const (
	// ManifestCryptoPlain — plaintext.
	ManifestCryptoPlain ManifestCrypto = "Plain"

	// ManifestCryptoMetadataOnly — only the metadata block is
	// encrypted. System fields stay in plaintext; Walk works without
	// keys.
	ManifestCryptoMetadataOnly ManifestCrypto = "MetadataOnly"

	// ManifestCryptoEnvelope — the entire serialised manifest is
	// encrypted into an opaque binary blob. ArtifactID stops being
	// content-addressable.
	ManifestCryptoEnvelope ManifestCrypto = "Envelope"
)

// DeletionPolicy is the deletion policy. A mutable parameter (with
// the caveat that DeletionPolicyLock applies).
type DeletionPolicy string

const (
	// DeletionPolicyNoDelete — WORM mode. Store.Delete returns
	// ErrDeletionForbidden; the GC Agent does not run on such a Store.
	DeletionPolicyNoDelete DeletionPolicy = "NoDelete"

	// DeletionPolicyRetention — deferred deletion. A blob with
	// ref_count = 0 becomes a GC candidate after RetentionPeriod.
	DeletionPolicyRetention DeletionPolicy = "Retention"

	// DeletionPolicyFree — immediate deletion governed by Two-Phase
	// Deletion (Mark-and-Sweep with Tombstone and TombstoneGracePeriod).
	DeletionPolicyFree DeletionPolicy = "Free"
)

// GCLeasePolicy is the policy for GC Agent coordination across
// processes. A mutable parameter.
type GCLeasePolicy string

const (
	// GCLeaseAuto — the engine derives the policy at OpenStore based
	// on topology (index backend + location.lock).
	GCLeaseAuto GCLeasePolicy = "Auto"

	// GCLeaseSingleHost — no coordination. Only valid on the SQLite
	// backend.
	GCLeaseSingleHost GCLeasePolicy = "SingleHost"

	// GCLeaseLeaderElection — coordination through a lease in
	// system.state/gc/lease. Required on a shared Postgres.
	GCLeaseLeaderElection GCLeasePolicy = "LeaderElection"
)

// PackAlignmentPolicy is the alignment policy for blobs inside a
// .pack volume.
type PackAlignmentPolicy int

const (
	// PackAlignmentAuto — chosen from Driver capabilities
	// (CapBlockAlign512 / CapBlockAlign4096).
	PackAlignmentAuto PackAlignmentPolicy = -1

	// PackAlignmentNone — no alignment; blobs are placed back to back.
	PackAlignmentNone PackAlignmentPolicy = 0

	PackAlignment512  PackAlignmentPolicy = 512
	PackAlignment4096 PackAlignmentPolicy = 4096
)

// VerifyOnReadPolicy controls explicit ContentHash verification on
// Get.
type VerifyOnReadPolicy string

const (
	// VerifyOnReadAuto — disables explicit re-hashing when the driver
	// reports CapNativeChecksum or the Pipeline ends with authenticated
	// encryption (the auth tag catches corruption on its own).
	VerifyOnReadAuto VerifyOnReadPolicy = "Auto"

	// VerifyOnReadForceEnabled — always re-hash. Paranoid mode.
	VerifyOnReadForceEnabled VerifyOnReadPolicy = "ForceEnabled"

	// VerifyOnReadDisabled — never re-hash. Trust the driver and the
	// Pipeline.
	VerifyOnReadDisabled VerifyOnReadPolicy = "Disabled"
)

// KDFParams are the parameters for deriving a KEK from a passphrase
// (Argon2id). They are applied at InitStore (or default values from
// the plugin are used). Once the descriptor is created, they are
// immutable — fixed in store.json.
type KDFParams struct {
	Time    uint32
	Memory  uint32
	Threads uint8
}

// StoreConfig is the full Store configuration. Some parameters are
// immutable (locked at InitStore), others are mutable (applied to
// new operations via UpdateConfig). Full breakdown:
// docs/4. API Reference/05 Configuration §5.1.
type StoreConfig struct {
	// Physical projection.
	PathTopology     PathTopology
	ManifestStorage  ManifestStorage
	BlobStorage      BlobStorage
	ManifestEncoding ManifestEncoding
	ManifestCrypto   ManifestCrypto
	PackAlignment    PackAlignmentPolicy
	EagerFetchLimit  int64

	// Pipeline and hashing.
	Pipeline            []string
	ContentHasher       ContentHashAlgorithm
	MetadataTransformer string
	VerifyOnRead        VerifyOnReadPolicy

	// Deletion and retention.
	DeletionPolicy       DeletionPolicy
	DeletionPolicyLock   bool // immutable; when true, removing NoDelete via UpdateConfig is forbidden.
	RetentionPeriod      time.Duration
	TombstoneGracePeriod time.Duration
	InlineBlobLimit      int64
	GCLeasePolicy        GCLeasePolicy

	// KDF (applied at InitStore with encryption).
	KDFParams *KDFParams
}

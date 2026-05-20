package domain

import "time"

// PathTopology is the topology of paths inside a Location. Immutable.
type PathTopology string

const (
	PathTopologyFlat    PathTopology = "Flat"
	PathTopologySharded PathTopology = "Sharded"
	PathTopologyNative  PathTopology = "Native"
)

// ManifestStorage controls where the manifest file lives. Immutable.
type ManifestStorage string

const (
	ManifestStorageRemote     ManifestStorage = "Remote"
	ManifestStorageLocal      ManifestStorage = "Local"
	ManifestStorageReplicated ManifestStorage = "Replicated"
)

// BlobStorage is the blob placement strategy.
type BlobStorage string

const (
	BlobStorageTarget         BlobStorage = "Target"
	BlobStorageInlineFallback BlobStorage = "InlineFallback"
	BlobStorageExternalRef    BlobStorage = "ExternalRef"
)

// ManifestEncoding is the on-disk serialisation format of a manifest.
type ManifestEncoding string

const (
	ManifestEncodingJSON   ManifestEncoding = "JSON"
	ManifestEncodingBinary ManifestEncoding = "Binary"
)

// ManifestCrypto controls manifest protection. Immutable.
//
// On-disk byte representation (header crypto flag) is stable
// across rename history: Sealed is byte 0x01, Paranoid is 0x02.
// Old in-flight configs containing the previous names
// ("Sealed", "Paranoid") are accepted by UnmarshalJSON for
// backwards compatibility — see manifest_crypto.go.
type ManifestCrypto string

const (
	ManifestCryptoPlain    ManifestCrypto = "Plain"
	ManifestCryptoSealed   ManifestCrypto = "Sealed"
	ManifestCryptoParanoid ManifestCrypto = "Paranoid"
)

// DeletionPolicy is the deletion policy.
type DeletionPolicy string

const (
	DeletionPolicyNoDelete  DeletionPolicy = "NoDelete"
	DeletionPolicyRetention DeletionPolicy = "Retention"
	DeletionPolicyFree      DeletionPolicy = "Free"
)

// GCLeasePolicy is the policy for GC Agent coordination.
type GCLeasePolicy string

const (
	GCLeaseAuto           GCLeasePolicy = "Auto"
	GCLeaseSingleHost     GCLeasePolicy = "SingleHost"
	GCLeaseLeaderElection GCLeasePolicy = "LeaderElection"
)

// PackAlignmentPolicy is the alignment policy for blobs inside a pack.
type PackAlignmentPolicy int

const (
	PackAlignmentAuto PackAlignmentPolicy = -1
	PackAlignmentNone PackAlignmentPolicy = 0
	PackAlignment512  PackAlignmentPolicy = 512
	PackAlignment4096 PackAlignmentPolicy = 4096
)

// VerifyOnReadPolicy controls explicit ContentHash verification on Get.
type VerifyOnReadPolicy string

const (
	VerifyOnReadAuto         VerifyOnReadPolicy = "Auto"
	VerifyOnReadForceEnabled VerifyOnReadPolicy = "ForceEnabled"
	VerifyOnReadDisabled     VerifyOnReadPolicy = "Disabled"
)

// KDFParams are the parameters for deriving a KEK.
type KDFParams struct {
	Time    uint32
	Memory  uint32
	Threads uint8
}

// StoreConfig is the full Store configuration.
type StoreConfig struct {
	PathTopology     PathTopology
	ManifestStorage  ManifestStorage
	BlobStorage      BlobStorage
	ManifestEncoding ManifestEncoding
	ManifestCrypto   ManifestCrypto
	PackAlignment    PackAlignmentPolicy
	EagerFetchLimit  int64

	Pipeline      []string
	ContentHasher ContentHashAlgorithm
	VerifyOnRead  VerifyOnReadPolicy

	DeletionPolicy       DeletionPolicy
	DeletionPolicyLock   bool
	RetentionPeriod      time.Duration
	TombstoneGracePeriod time.Duration
	InlineBlobLimit      int64
	GCLeasePolicy        GCLeasePolicy

	KDFParams *KDFParams
}

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

// EncryptedDedup controls deduplication of ENCRYPTED blobs. Immutable.
//
// It has no effect on Plain (unencrypted) blobs: their dedup key is
// always (ContentHash, OriginalSize) per ADR-29. For an encrypting
// store it governs whether two writes of the same plaintext under
// the same key can collapse to one physical blob. See ADR-58.
type EncryptedDedup string

const (
	// EncryptedDedupDisabled — random IV per write. The same
	// plaintext yields different ciphertext, a different BlobRef,
	// a different address: encrypted blobs never deduplicate. Full
	// AEAD semantics, no equality leak. Default for an encrypting
	// store.
	EncryptedDedupDisabled EncryptedDedup = "Disabled"
	// EncryptedDedupConvergent — IV = KDF(ContentHash, KeyID),
	// realised per-segment as HMAC-SHA256(DEK, segHash ‖ KeyID ‖
	// index) (ADR-59). One plaintext under one key yields one
	// ciphertext, one BlobRef: encrypted blobs deduplicate, at the
	// cost of leaking content equality to a storage observer. Wired
	// in R8 (ADR-58/59).
	EncryptedDedupConvergent EncryptedDedup = "Convergent"
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
	EncryptedDedup   EncryptedDedup
	PackAlignment    PackAlignmentPolicy
	EagerFetchLimit  int64

	Pipeline      []string
	ContentHasher ContentHashAlgorithm
	VerifyOnRead  VerifyOnReadPolicy

	// SegmentSize is the plaintext segment size of the segmented
	// AEAD blob format (ADR-59), in bytes. Immutable: ciphertext
	// reproducibility under EncryptedDedup=Convergent (and therefore
	// dedup of encrypted blobs and chunks) requires a stable value.
	// Zero is ignored for a Plain store and defaulted to
	// DefaultSegmentSize (≈1 MiB) for an encrypting store. Bounds:
	// MinSegmentSize..MaxSegmentSize.
	SegmentSize int

	DeletionPolicy       DeletionPolicy
	DeletionPolicyLock   bool
	RetentionPeriod      time.Duration
	TombstoneGracePeriod time.Duration
	InlineBlobLimit      int64
	GCLeasePolicy        GCLeasePolicy

	// DefaultPutNamespace is the namespace a Put falls back to when its
	// options leave the namespace empty. Mutable (changeable via
	// UpdateConfig like other policy fields) and not part of the
	// immutable format identity: it only influences which namespace an
	// otherwise-unnamespaced Put resolves to at write time. Each
	// artifact records its resolved namespace in its own manifest, so
	// changing this never reinterprets already-stored artifacts — new
	// unnamespaced Puts simply land under the new value. Empty (the
	// default) means an unnamespaced Put stays in the store's own
	// default namespace.
	DefaultPutNamespace string

	KDFParams *KDFParams
}

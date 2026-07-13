package storeconfig

import "time"

// StoreConfig is the full Store configuration — the aggregate of every
// tunable parameter. The parameter types themselves are declared
// alongside, grouped by concern: crypto.go (protection, dedup, hashing,
// KDF), layout.go (on-disk placement), policy.go (deletion, GC, session,
// verify) and identity.go (handle coalescing).
type StoreConfig struct {
	PathTopology     PathTopology
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
	// IdentityMode controls handle coalescing (ADR-73). Immutable.
	// Empty = IdentityModeUnique.
	IdentityMode IdentityMode

	DeletionPolicy       DeletionPolicy
	DeletionPolicyLock   bool
	RetentionPeriod      time.Duration
	TombstoneGracePeriod time.Duration
	InlineBlobLimit      int64
	GCLeasePolicy        GCLeasePolicy
	// SessionOverrides is the class-II admin knob over class-III
	// client overrides (ADR-110). Empty defaults to Allow.
	SessionOverrides SessionOverridesPolicy
	// MaxArtifactSize caps a single artifact's payload in bytes
	// (class II governance; 0 = unlimited). Enforced as a streaming
	// guard on the Put paths — the payload aborts with
	// errs.ErrArtifactTooLarge once the limit is crossed.
	MaxArtifactSize int64

	KDFParams *KDFParams
}

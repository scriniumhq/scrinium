package config

import (
	"fmt"
	"time"

	"scrinium.dev/errs"
)

// The field registry: ONE declaration per StoreConfig field — its name,
// ADR-110 class, connection behaviour, a typed getter/setter, a
// validator, and a default. Every per-field operation is derived from
// this table by the traversal engine in package fieldkit; there is no
// second hand-written enumeration of the fields anywhere.
//
// This file is the rulebook. To add a config field you add one row here
// (and its struct field in config.StoreConfig) — nothing in fieldkit.
// The row's slots:
//
//	FName/FClass/FConn — identity, class, connection fate
//	Get / Set          — read/write the field on a StoreConfig
//	Check              — enum(...) / minVal(...) / rangeVal(...) etc.
//	DefaultTo          — unconditional value applied when the field is zero
//	DefaultFn          — conditional / zero-meaningful default (wins over DefaultTo)
//
// No reflection: the getter is a typed closure, the allowed values are
// typed constructors — a typo does not compile.

var registry = []fieldDesc{
	// --- Class I: immutable ---
	field[PathTopology]{
		FName: "PathTopology", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:       func(c StoreConfig) PathTopology { return c.PathTopology },
		Set:       func(c *StoreConfig, v PathTopology) { c.PathTopology = v },
		Check:     enum(PathTopologyFlat, PathTopologySharded),
		DefaultTo: PathTopologySharded,
	},
	field[ManifestEncoding]{
		FName: "ManifestEncoding", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:       func(c StoreConfig) ManifestEncoding { return c.ManifestEncoding },
		Set:       func(c *StoreConfig, v ManifestEncoding) { c.ManifestEncoding = v },
		Check:     encodingCheck,
		DefaultTo: ManifestEncodingJSON,
	},
	field[ManifestCrypto]{
		FName: "ManifestCrypto", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:       func(c StoreConfig) ManifestCrypto { return c.ManifestCrypto },
		Set:       func(c *StoreConfig, v ManifestCrypto) { c.ManifestCrypto = v },
		Check:     enum(ManifestCryptoPlain, ManifestCryptoSealed, ManifestCryptoParanoid),
		DefaultTo: ManifestCryptoPlain,
	},
	field[EncryptedDedup]{
		FName: "EncryptedDedup", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:   func(c StoreConfig) EncryptedDedup { return c.EncryptedDedup },
		Set:   func(c *StoreConfig, v EncryptedDedup) { c.EncryptedDedup = v },
		Check: enum(EncryptedDedupDisabled, EncryptedDedupConvergent),
		// ADR-58: default Disabled only for an encrypting store; a Plain
		// store leaves it empty (crypto-identity empty, dedup key
		// degrades to (ContentHash, OriginalSize)).
		DefaultFn: func(c StoreConfig) (EncryptedDedup, bool) {
			if c.EncryptedDedup == "" && isEncryptingConfig(c) {
				return EncryptedDedupDisabled, true
			}
			return "", false
		},
	},
	field[ContentHashAlgorithm]{
		FName: "ContentHasher", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:       func(c StoreConfig) ContentHashAlgorithm { return c.ContentHasher },
		Set:       func(c *StoreConfig, v ContentHashAlgorithm) { c.ContentHasher = v },
		Check:     enum(HashSHA256, HashBLAKE3),
		DefaultTo: HashSHA256,
	},
	field[int]{
		FName: "SegmentSize", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:   func(c StoreConfig) int { return c.SegmentSize },
		Set:   func(c *StoreConfig, v int) { c.SegmentSize = v },
		Check: rangeVal("SegmentSize", MinSegmentSize, MaxSegmentSize),
		// ADR-59: the segmented AEAD format needs a segment size only
		// when the store encrypts; a Plain store leaves it zero.
		DefaultFn: func(c StoreConfig) (int, bool) {
			if c.SegmentSize == 0 && isEncryptingConfig(c) {
				return DefaultSegmentSize, true
			}
			return 0, false
		},
	},
	field[IdentityMode]{
		FName: "IdentityMode", FClass: classImmutable, FConn: connRefusedImmutable,
		Get:   func(c StoreConfig) IdentityMode { return c.IdentityMode },
		Set:   func(c *StoreConfig, v IdentityMode) { c.IdentityMode = v },
		Check: enum(IdentityModeUnique, IdentityModeCoalesced),
		// Empty = IdentityModeUnique; left un-defaulted so the zero
		// stays meaningful downstream (matches prior ApplyDefaults).
	},
	field[bool]{
		FName: "DeletionPolicyLock", FClass: classImmutable, FConn: connRefusedImmutable,
		Get: func(c StoreConfig) bool { return c.DeletionPolicyLock },
		Set: func(c *StoreConfig, v bool) { c.DeletionPolicyLock = v },
		// bool: no value-level validation, no default.
	},

	// --- Class II: governance ---
	field[DeletionPolicy]{
		FName: "DeletionPolicy", FClass: classGovernance, FConn: connRefusedGovernance,
		Get:       func(c StoreConfig) DeletionPolicy { return c.DeletionPolicy },
		Set:       func(c *StoreConfig, v DeletionPolicy) { c.DeletionPolicy = v },
		Check:     enum(DeletionPolicyFree, DeletionPolicyRetention, DeletionPolicyNoDelete),
		DefaultTo: DeletionPolicyFree,
	},
	field[time.Duration]{
		FName: "RetentionPeriod", FClass: classGovernance, FConn: connRefusedGovernance,
		Get:   func(c StoreConfig) time.Duration { return c.RetentionPeriod },
		Set:   func(c *StoreConfig, v time.Duration) { c.RetentionPeriod = v },
		Check: minVal("RetentionPeriod", MinRetentionPeriod),
		// Zero = feature off; not defaulted.
	},
	field[time.Duration]{
		FName: "TombstoneGracePeriod", FClass: classGovernance, FConn: connRefusedGovernance,
		Get: func(c StoreConfig) time.Duration { return c.TombstoneGracePeriod },
		Set: func(c *StoreConfig, v time.Duration) { c.TombstoneGracePeriod = v },
		Check: withSentinel(
			minVal("TombstoneGracePeriod", MinTombstoneGracePeriod),
			errs.ErrInvalidTombstoneGracePeriod),
		// Unconditional 24h, but a Duration's zero-fill needs an
		// explicit fn (DefaultTo's "non-zero enum" convention is for
		// string enums; keep the intent obvious here).
		DefaultFn: func(c StoreConfig) (time.Duration, bool) {
			if c.TombstoneGracePeriod == 0 {
				return 24 * time.Hour, true
			}
			return 0, false
		},
	},
	field[GCLeasePolicy]{
		FName: "GCLeasePolicy", FClass: classGovernance, FConn: connRefusedGovernance,
		Get:       func(c StoreConfig) GCLeasePolicy { return c.GCLeasePolicy },
		Set:       func(c *StoreConfig, v GCLeasePolicy) { c.GCLeasePolicy = v },
		Check:     enum(GCLeaseAuto, GCLeaseSingleHost, GCLeaseLeaderElection),
		DefaultTo: GCLeaseAuto,
	},
	field[SessionOverridesPolicy]{
		FName: "SessionOverrides", FClass: classGovernance, FConn: connRefusedGovernance,
		Get:       func(c StoreConfig) SessionOverridesPolicy { return c.SessionOverrides },
		Set:       func(c *StoreConfig, v SessionOverridesPolicy) { c.SessionOverrides = v },
		Check:     enum(SessionOverridesAllow, SessionOverridesDeny),
		DefaultTo: SessionOverridesAllow,
	},
	field[int64]{
		FName: "MaxArtifactSize", FClass: classGovernance, FConn: connRefusedGovernance,
		Get:   func(c StoreConfig) int64 { return c.MaxArtifactSize },
		Set:   func(c *StoreConfig, v int64) { c.MaxArtifactSize = v },
		Check: nonNegative[int64]("MaxArtifactSize"),
		// Zero = unlimited; not defaulted.
	},

	// --- Class III: session ---
	field[BlobStorage]{
		FName: "BlobStorage", FClass: classSession, FConn: connOverlay,
		Get:       func(c StoreConfig) BlobStorage { return c.BlobStorage },
		Set:       func(c *StoreConfig, v BlobStorage) { c.BlobStorage = v },
		Check:     enum(BlobStorageTarget, BlobStorageInline),
		DefaultTo: BlobStorageTarget,
	},
	field[VerifyOnReadPolicy]{
		FName: "VerifyOnRead", FClass: classSession, FConn: connOverlay,
		Get:       func(c StoreConfig) VerifyOnReadPolicy { return c.VerifyOnRead },
		Set:       func(c *StoreConfig, v VerifyOnReadPolicy) { c.VerifyOnRead = v },
		Check:     enum(VerifyOnReadAuto, VerifyOnReadForceEnabled, VerifyOnReadDisabled),
		DefaultTo: VerifyOnReadAuto,
	},
	field[int64]{
		FName: "InlineBlobLimit", FClass: classSession, FConn: connOverlay,
		Get:   func(c StoreConfig) int64 { return c.InlineBlobLimit },
		Set:   func(c *StoreConfig, v int64) { c.InlineBlobLimit = v },
		Check: maxVal[int64]("InlineBlobLimit", MaxInlineBlobLimit),
		// Zero = feature off; not defaulted.
	},
	field[PackAlignmentPolicy]{
		FName: "PackAlignment", FClass: classSession, FConn: connOverlay,
		Get:   func(c StoreConfig) PackAlignmentPolicy { return c.PackAlignment },
		Set:   func(c *StoreConfig, v PackAlignmentPolicy) { c.PackAlignment = v },
		Check: enum(PackAlignmentAuto, PackAlignmentNone, PackAlignment512, PackAlignment4096),
		// Zero literal is also PackAlignmentNone; promote zero to Auto
		// (derive from the Driver) rather than "no alignment". This is
		// why it needs DefaultFn — DefaultTo would refuse to touch a
		// value that equals the field's zero.
		DefaultFn: func(c StoreConfig) (PackAlignmentPolicy, bool) {
			if c.PackAlignment == 0 {
				return PackAlignmentAuto, true
			}
			return 0, false
		},
	},
	field[int64]{
		FName: "EagerFetchLimit", FClass: classSession, FConn: connOverlay,
		Get: func(c StoreConfig) int64 { return c.EagerFetchLimit },
		Set: func(c *StoreConfig, v int64) { c.EagerFetchLimit = v },
		// No bound, no default; row carries class/conn for the overlay.
	},

	// --- Class III cross-field: Pipeline (hand-written) ---
	pipelineDesc{},
}

// encodingCheck is ManifestEncoding's validator: JSON is fine, Binary
// is recognised-but-deferred (a distinct message from invalid), and
// anything else is invalid. Not a plain enum because of the deferred
// member.
func encodingCheck(e ManifestEncoding) error {
	switch e {
	case "", ManifestEncodingJSON:
		return nil
	case ManifestEncodingBinary:
		return fmt.Errorf("%w: ManifestEncoding=Binary deferred", errs.ErrInvalidConfig)
	default:
		return errs.ErrInvalidConfig
	}
}

// pipelineDesc is Pipeline's hand-written row: []string is not
// comparable (so the generic field[T] can't hold it), and its validity
// is the crypto-tail rule (ADR-110) — a derivative of the class-I
// pipeline of the ACTIVE config, not a value-local check. Validation of
// the tail therefore happens in PlanConnection (validateCryptoTail),
// where both req and active are in hand; here validate is a no-op and
// diverges reports a plain slice difference.
type pipelineDesc struct{}

func (pipelineDesc) Name() string       { return "Pipeline" }
func (pipelineDesc) Class() FieldClass  { return classSession }
func (pipelineDesc) Conn() ConnBehavior { return connDerived }

func (pipelineDesc) Validate(StoreConfig) error { return nil }

func (pipelineDesc) ApplyDefault(*StoreConfig) {} // Pipeline is never defaulted

func (pipelineDesc) Diverges(req, active StoreConfig) (string, bool) {
	if len(req.Pipeline) == 0 || equalPipelines(req.Pipeline, active.Pipeline) {
		return "", false
	}
	return fmt.Sprintf("Pipeline: requested %v, active %v", req.Pipeline, active.Pipeline), true
}

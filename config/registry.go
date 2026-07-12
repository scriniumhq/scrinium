package config

import (
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// The field registry: ONE declaration per StoreConfig field — its name,
// ADR-110 class, connection behaviour, a typed getter/setter, a
// validator, and a default. Every per-field operation is derived from
// this table by the traversal engine in package fieldkit; there is no
// second hand-written enumeration of the fields anywhere.
//
// This file is the rulebook. To add a config field you add one row here
// (and its struct field in domain.StoreConfig) — nothing in fieldkit.
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
	field[domain.PathTopology]{
		FName: "PathTopology", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:       func(c domain.StoreConfig) domain.PathTopology { return c.PathTopology },
		Set:       func(c *domain.StoreConfig, v domain.PathTopology) { c.PathTopology = v },
		Check:     enum(domain.PathTopologyFlat, domain.PathTopologySharded),
		DefaultTo: domain.PathTopologySharded,
	},
	field[domain.ManifestEncoding]{
		FName: "ManifestEncoding", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:       func(c domain.StoreConfig) domain.ManifestEncoding { return c.ManifestEncoding },
		Set:       func(c *domain.StoreConfig, v domain.ManifestEncoding) { c.ManifestEncoding = v },
		Check:     encodingCheck,
		DefaultTo: domain.ManifestEncodingJSON,
	},
	field[domain.ManifestCrypto]{
		FName: "ManifestCrypto", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:       func(c domain.StoreConfig) domain.ManifestCrypto { return c.ManifestCrypto },
		Set:       func(c *domain.StoreConfig, v domain.ManifestCrypto) { c.ManifestCrypto = v },
		Check:     enum(domain.ManifestCryptoPlain, domain.ManifestCryptoSealed, domain.ManifestCryptoParanoid),
		DefaultTo: domain.ManifestCryptoPlain,
	},
	field[domain.EncryptedDedup]{
		FName: "EncryptedDedup", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.EncryptedDedup { return c.EncryptedDedup },
		Set:   func(c *domain.StoreConfig, v domain.EncryptedDedup) { c.EncryptedDedup = v },
		Check: enum(domain.EncryptedDedupDisabled, domain.EncryptedDedupConvergent),
		// ADR-58: default Disabled only for an encrypting store; a Plain
		// store leaves it empty (crypto-identity empty, dedup key
		// degrades to (ContentHash, OriginalSize)).
		DefaultFn: func(c domain.StoreConfig) (domain.EncryptedDedup, bool) {
			if c.EncryptedDedup == "" && isEncryptingConfig(c) {
				return domain.EncryptedDedupDisabled, true
			}
			return "", false
		},
	},
	field[domain.ContentHashAlgorithm]{
		FName: "ContentHasher", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:       func(c domain.StoreConfig) domain.ContentHashAlgorithm { return c.ContentHasher },
		Set:       func(c *domain.StoreConfig, v domain.ContentHashAlgorithm) { c.ContentHasher = v },
		Check:     enum(domain.HashSHA256, domain.HashBLAKE3),
		DefaultTo: domain.HashSHA256,
	},
	field[int]{
		FName: "SegmentSize", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) int { return c.SegmentSize },
		Set:   func(c *domain.StoreConfig, v int) { c.SegmentSize = v },
		Check: rangeVal("SegmentSize", domain.MinSegmentSize, domain.MaxSegmentSize),
		// ADR-59: the segmented AEAD format needs a segment size only
		// when the store encrypts; a Plain store leaves it zero.
		DefaultFn: func(c domain.StoreConfig) (int, bool) {
			if c.SegmentSize == 0 && isEncryptingConfig(c) {
				return domain.DefaultSegmentSize, true
			}
			return 0, false
		},
	},
	field[domain.IdentityMode]{
		FName: "IdentityMode", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.IdentityMode { return c.IdentityMode },
		Set:   func(c *domain.StoreConfig, v domain.IdentityMode) { c.IdentityMode = v },
		Check: enum(domain.IdentityModeUnique, domain.IdentityModeCoalesced),
		// Empty = IdentityModeUnique; left un-defaulted so the zero
		// stays meaningful downstream (matches prior ApplyDefaults).
	},
	field[bool]{
		FName: "DeletionPolicyLock", FClass: ClassImmutable, FConn: ConnRefusedImmutable,
		Get: func(c domain.StoreConfig) bool { return c.DeletionPolicyLock },
		Set: func(c *domain.StoreConfig, v bool) { c.DeletionPolicyLock = v },
		// bool: no value-level validation, no default.
	},

	// --- Class II: governance ---
	field[domain.DeletionPolicy]{
		FName: "DeletionPolicy", FClass: ClassGovernance, FConn: ConnRefusedGovernance,
		Get:       func(c domain.StoreConfig) domain.DeletionPolicy { return c.DeletionPolicy },
		Set:       func(c *domain.StoreConfig, v domain.DeletionPolicy) { c.DeletionPolicy = v },
		Check:     enum(domain.DeletionPolicyFree, domain.DeletionPolicyRetention, domain.DeletionPolicyNoDelete),
		DefaultTo: domain.DeletionPolicyFree,
	},
	field[time.Duration]{
		FName: "RetentionPeriod", FClass: ClassGovernance, FConn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) time.Duration { return c.RetentionPeriod },
		Set:   func(c *domain.StoreConfig, v time.Duration) { c.RetentionPeriod = v },
		Check: minVal("RetentionPeriod", domain.MinRetentionPeriod),
		// Zero = feature off; not defaulted.
	},
	field[time.Duration]{
		FName: "TombstoneGracePeriod", FClass: ClassGovernance, FConn: ConnRefusedGovernance,
		Get: func(c domain.StoreConfig) time.Duration { return c.TombstoneGracePeriod },
		Set: func(c *domain.StoreConfig, v time.Duration) { c.TombstoneGracePeriod = v },
		Check: withSentinel(
			minVal("TombstoneGracePeriod", domain.MinTombstoneGracePeriod),
			errs.ErrInvalidTombstoneGracePeriod),
		// Unconditional 24h, but a Duration's zero-fill needs an
		// explicit fn (DefaultTo's "non-zero enum" convention is for
		// string enums; keep the intent obvious here).
		DefaultFn: func(c domain.StoreConfig) (time.Duration, bool) {
			if c.TombstoneGracePeriod == 0 {
				return 24 * time.Hour, true
			}
			return 0, false
		},
	},
	field[domain.GCLeasePolicy]{
		FName: "GCLeasePolicy", FClass: ClassGovernance, FConn: ConnRefusedGovernance,
		Get:       func(c domain.StoreConfig) domain.GCLeasePolicy { return c.GCLeasePolicy },
		Set:       func(c *domain.StoreConfig, v domain.GCLeasePolicy) { c.GCLeasePolicy = v },
		Check:     enum(domain.GCLeaseAuto, domain.GCLeaseSingleHost, domain.GCLeaseLeaderElection),
		DefaultTo: domain.GCLeaseAuto,
	},
	field[domain.SessionOverridesPolicy]{
		FName: "SessionOverrides", FClass: ClassGovernance, FConn: ConnRefusedGovernance,
		Get:       func(c domain.StoreConfig) domain.SessionOverridesPolicy { return c.SessionOverrides },
		Set:       func(c *domain.StoreConfig, v domain.SessionOverridesPolicy) { c.SessionOverrides = v },
		Check:     enum(domain.SessionOverridesAllow, domain.SessionOverridesDeny),
		DefaultTo: domain.SessionOverridesAllow,
	},
	field[int64]{
		FName: "MaxArtifactSize", FClass: ClassGovernance, FConn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) int64 { return c.MaxArtifactSize },
		Set:   func(c *domain.StoreConfig, v int64) { c.MaxArtifactSize = v },
		Check: nonNegative[int64]("MaxArtifactSize"),
		// Zero = unlimited; not defaulted.
	},

	// --- Class III: session ---
	field[domain.BlobStorage]{
		FName: "BlobStorage", FClass: ClassSession, FConn: ConnOverlay,
		Get:       func(c domain.StoreConfig) domain.BlobStorage { return c.BlobStorage },
		Set:       func(c *domain.StoreConfig, v domain.BlobStorage) { c.BlobStorage = v },
		Check:     enum(domain.BlobStorageTarget, domain.BlobStorageInline),
		DefaultTo: domain.BlobStorageTarget,
	},
	field[domain.VerifyOnReadPolicy]{
		FName: "VerifyOnRead", FClass: ClassSession, FConn: ConnOverlay,
		Get:       func(c domain.StoreConfig) domain.VerifyOnReadPolicy { return c.VerifyOnRead },
		Set:       func(c *domain.StoreConfig, v domain.VerifyOnReadPolicy) { c.VerifyOnRead = v },
		Check:     enum(domain.VerifyOnReadAuto, domain.VerifyOnReadForceEnabled, domain.VerifyOnReadDisabled),
		DefaultTo: domain.VerifyOnReadAuto,
	},
	field[int64]{
		FName: "InlineBlobLimit", FClass: ClassSession, FConn: ConnOverlay,
		Get:   func(c domain.StoreConfig) int64 { return c.InlineBlobLimit },
		Set:   func(c *domain.StoreConfig, v int64) { c.InlineBlobLimit = v },
		Check: maxVal[int64]("InlineBlobLimit", domain.MaxInlineBlobLimit),
		// Zero = feature off; not defaulted.
	},
	field[domain.PackAlignmentPolicy]{
		FName: "PackAlignment", FClass: ClassSession, FConn: ConnOverlay,
		Get:   func(c domain.StoreConfig) domain.PackAlignmentPolicy { return c.PackAlignment },
		Set:   func(c *domain.StoreConfig, v domain.PackAlignmentPolicy) { c.PackAlignment = v },
		Check: enum(domain.PackAlignmentAuto, domain.PackAlignmentNone, domain.PackAlignment512, domain.PackAlignment4096),
		// Zero literal is also PackAlignmentNone; promote zero to Auto
		// (derive from the Driver) rather than "no alignment". This is
		// why it needs DefaultFn — DefaultTo would refuse to touch a
		// value that equals the field's zero.
		DefaultFn: func(c domain.StoreConfig) (domain.PackAlignmentPolicy, bool) {
			if c.PackAlignment == 0 {
				return domain.PackAlignmentAuto, true
			}
			return 0, false
		},
	},
	field[int64]{
		FName: "EagerFetchLimit", FClass: ClassSession, FConn: ConnOverlay,
		Get: func(c domain.StoreConfig) int64 { return c.EagerFetchLimit },
		Set: func(c *domain.StoreConfig, v int64) { c.EagerFetchLimit = v },
		// No bound, no default; row carries class/conn for the overlay.
	},

	// --- Class III cross-field: Pipeline (hand-written) ---
	pipelineDesc{},
}

// encodingCheck is ManifestEncoding's validator: JSON is fine, Binary
// is recognised-but-deferred (a distinct message from invalid), and
// anything else is invalid. Not a plain enum because of the deferred
// member.
func encodingCheck(e domain.ManifestEncoding) error {
	switch e {
	case "", domain.ManifestEncodingJSON:
		return nil
	case domain.ManifestEncodingBinary:
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
func (pipelineDesc) Class() FieldClass  { return ClassSession }
func (pipelineDesc) Conn() ConnBehavior { return ConnDerived }

func (pipelineDesc) Validate(domain.StoreConfig) error { return nil }

func (pipelineDesc) ApplyDefault(*domain.StoreConfig) {} // Pipeline is never defaulted

func (pipelineDesc) Diverges(req, active domain.StoreConfig) (string, bool) {
	if len(req.Pipeline) == 0 || equalPipelines(req.Pipeline, active.Pipeline) {
		return "", false
	}
	return fmt.Sprintf("Pipeline: requested %v, active %v", req.Pipeline, active.Pipeline), true
}

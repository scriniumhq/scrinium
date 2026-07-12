package config

import (
	"cmp"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Field-descriptor registry (config review R-g / S-11). ONE declaration
// per StoreConfig field — name, ADR-110 class, connection behaviour, a
// typed getter, and a validator — from which every per-field operation
// is derived: ValidateImmutable loops the table calling validate;
// divergentGovernance / divergentSession loop it filtered by class
// calling diverges. There is no second hand-written enumeration of the
// fields anywhere: "forgot the field in one of the validators" stops
// being a possible class of bug because there is only one list.
//
// No reflection: a field binds to its struct field through a typed
// getter closure, and its allowed values / bounds are typed generic
// constructors. A typo does not compile.

// fieldDesc is the type-erased row the registry stores. field[T]
// implements it for every concrete field type, so fields of different
// Go types coexist in one slice.
type fieldDesc interface {
	name() string
	class() FieldClass
	conn() ConnBehavior
	// validate checks this field's value in cfg (enum / bounds).
	validate(cfg domain.StoreConfig) error
	// diverges reports whether req's populated value differs from
	// active's, with a human message. Zero (unset) never diverges.
	diverges(req, active domain.StoreConfig) (string, bool)
}

// field is one typed row. T is the field's Go type (domain.PathTopology,
// time.Duration, int64, …). Get reads it from a StoreConfig; Check
// validates a value (nil = nothing to validate); Fmt renders it for a
// divergence message (nil = %v).
type field[T comparable] struct {
	Name  string
	Class FieldClass
	Conn  ConnBehavior
	Get   func(domain.StoreConfig) T
	Check func(T) error
	Fmt   func(T) string
}

func (f field[T]) name() string       { return f.Name }
func (f field[T]) class() FieldClass  { return f.Class }
func (f field[T]) conn() ConnBehavior { return f.Conn }

func (f field[T]) validate(cfg domain.StoreConfig) error {
	if f.Check == nil {
		return nil
	}
	return f.Check(f.Get(cfg))
}

func (f field[T]) diverges(req, active domain.StoreConfig) (string, bool) {
	var zero T
	rv := f.Get(req)
	if rv == zero || rv == f.Get(active) {
		return "", false
	}
	return fmt.Sprintf("%s: requested %s, active %s", f.Name, f.render(rv), f.render(f.Get(active))), true
}

func (f field[T]) render(v T) string {
	if f.Fmt != nil {
		return f.Fmt(v)
	}
	return fmt.Sprintf("%v", v)
}

// --- standard validator constructors ---
// Each returns a func(T) error for the Check slot. Zero (the Go zero
// value) always passes: a zero is "field omitted / not applicable"
// (a Plain store leaves crypto fields zero, a not-yet-defaulted config
// leaves everything zero). Callers opt into a check by setting a value.

// enum accepts a value from a fixed set (or zero).
func enum[T comparable](allowed ...T) func(T) error {
	return func(v T) error {
		var zero T
		if v == zero {
			return nil
		}
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("%w: got %v, want one of %v", errs.ErrInvalidConfig, v, allowed)
	}
}

// minVal enforces a lower bound (zero = off).
func minVal[T cmp.Ordered](name string, min T) func(T) error {
	return func(v T) error {
		var zero T
		if v != zero && v < min {
			return fmt.Errorf("%w: %s=%v below minimum %v", errs.ErrInvalidConfig, name, v, min)
		}
		return nil
	}
}

// maxVal enforces an upper bound (zero = off).
func maxVal[T cmp.Ordered](name string, max T) func(T) error {
	return func(v T) error {
		var zero T
		if v != zero && v > max {
			return fmt.Errorf("%w: %s=%v above maximum %v", errs.ErrInvalidConfig, name, v, max)
		}
		return nil
	}
}

// rangeVal enforces [min, max] (zero = off) in one check.
func rangeVal[T cmp.Ordered](name string, min, max T) func(T) error {
	return func(v T) error {
		var zero T
		if v != zero && (v < min || v > max) {
			return fmt.Errorf("%w: %s=%v out of range [%v, %v]", errs.ErrInvalidConfig, name, v, min, max)
		}
		return nil
	}
}

// nonNegative refuses negative values (zero passes — e.g. 0 = unlimited).
func nonNegative[T cmp.Ordered](name string) func(T) error {
	return func(v T) error {
		var zero T
		if v < zero {
			return fmt.Errorf("%w: %s=%v is negative", errs.ErrInvalidConfig, name, v)
		}
		return nil
	}
}

// withSentinel replaces a check's default ErrInvalidConfig with a
// dedicated sentinel (e.g. TombstoneGracePeriod → ErrInvalidTombstoneGracePeriod).
func withSentinel[T any](check func(T) error, sentinel error) func(T) error {
	return func(v T) error {
		if check(v) != nil {
			return sentinel
		}
		return nil
	}
}

// and runs several checks in order (first failure wins).
func and[T any](checks ...func(T) error) func(T) error {
	return func(v T) error {
		for _, c := range checks {
			if err := c(v); err != nil {
				return err
			}
		}
		return nil
	}
}

// registryRows returns (name, class, conn) for every field in the
// registry — the projection the conformance tests assert against. Kept
// here (not in _test.go) because it reads the unexported registry;
// exported so the test file, in package config, can reach it plainly.
func registryRows() []struct {
	Name  string
	Class FieldClass
	Conn  ConnBehavior
} {
	out := make([]struct {
		Name  string
		Class FieldClass
		Conn  ConnBehavior
	}, len(registry))
	for i, f := range registry {
		out[i] = struct {
			Name  string
			Class FieldClass
			Conn  ConnBehavior
		}{f.name(), f.class(), f.conn()}
	}
	return out
}

// registry is the single source: every domain.StoreConfig field, in
// struct order, each declared once with its class, connection
// behaviour, typed getter and validator.
//
// Fields NOT in this slice are handled out of band and must be
// accounted for elsewhere (the conformance test checks the union):
//   - Pipeline — class III, []string, cross-field crypto-tail rule →
//     pipelineDesc below (hand-written; a slice is not comparable and
//     its validity depends on another field).
//   - DeletionPolicyLock — class I bool; a bool has no enum/bounds to
//     validate and its divergence is folded into the deletion-policy
//     lock rule, not a standalone field check → declared with a nil
//     Check purely to carry its class/conn for divergence.
//   - KDFParams — ConnIgnored, input-only at InitStore, never compared
//     or validated as config → not represented here at all.
//   - ManifestEncoding — enum with a deferred member (Binary is
//     recognised but refused as not-yet-shipped, distinct from
//     invalid) → hand-written encodingCheck.
var registry = []fieldDesc{
	// --- Class I: immutable ---
	field[domain.PathTopology]{
		Name: "PathTopology", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.PathTopology { return c.PathTopology },
		Check: enum(domain.PathTopologyFlat, domain.PathTopologySharded),
	},
	field[domain.ManifestEncoding]{
		Name: "ManifestEncoding", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.ManifestEncoding { return c.ManifestEncoding },
		Check: encodingCheck,
	},
	field[domain.ManifestCrypto]{
		Name: "ManifestCrypto", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.ManifestCrypto { return c.ManifestCrypto },
		Check: enum(domain.ManifestCryptoPlain, domain.ManifestCryptoSealed, domain.ManifestCryptoParanoid),
	},
	field[domain.EncryptedDedup]{
		Name: "EncryptedDedup", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.EncryptedDedup { return c.EncryptedDedup },
		Check: enum(domain.EncryptedDedupDisabled, domain.EncryptedDedupConvergent),
	},
	field[domain.ContentHashAlgorithm]{
		Name: "ContentHasher", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.ContentHashAlgorithm { return c.ContentHasher },
		Check: enum(domain.HashSHA256, domain.HashBLAKE3),
	},
	field[int]{
		Name: "SegmentSize", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) int { return c.SegmentSize },
		Check: rangeVal("SegmentSize", domain.MinSegmentSize, domain.MaxSegmentSize),
	},
	field[domain.IdentityMode]{
		Name: "IdentityMode", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get:   func(c domain.StoreConfig) domain.IdentityMode { return c.IdentityMode },
		Check: enum(domain.IdentityModeUnique, domain.IdentityModeCoalesced),
	},
	field[bool]{
		Name: "DeletionPolicyLock", Class: ClassImmutable, Conn: ConnRefusedImmutable,
		Get: func(c domain.StoreConfig) bool { return c.DeletionPolicyLock },
		// bool: no value-level validation; class/conn carry the row.
	},

	// --- Class II: governance ---
	field[domain.DeletionPolicy]{
		Name: "DeletionPolicy", Class: ClassGovernance, Conn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) domain.DeletionPolicy { return c.DeletionPolicy },
		Check: enum(domain.DeletionPolicyFree, domain.DeletionPolicyRetention, domain.DeletionPolicyNoDelete),
	},
	field[time.Duration]{
		Name: "RetentionPeriod", Class: ClassGovernance, Conn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) time.Duration { return c.RetentionPeriod },
		Check: minVal("RetentionPeriod", domain.MinRetentionPeriod),
	},
	field[time.Duration]{
		Name: "TombstoneGracePeriod", Class: ClassGovernance, Conn: ConnRefusedGovernance,
		Get: func(c domain.StoreConfig) time.Duration { return c.TombstoneGracePeriod },
		Check: withSentinel(
			minVal("TombstoneGracePeriod", domain.MinTombstoneGracePeriod),
			errs.ErrInvalidTombstoneGracePeriod),
	},
	field[domain.GCLeasePolicy]{
		Name: "GCLeasePolicy", Class: ClassGovernance, Conn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) domain.GCLeasePolicy { return c.GCLeasePolicy },
		Check: enum(domain.GCLeaseAuto, domain.GCLeaseSingleHost, domain.GCLeaseLeaderElection),
	},
	field[domain.SessionOverridesPolicy]{
		Name: "SessionOverrides", Class: ClassGovernance, Conn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) domain.SessionOverridesPolicy { return c.SessionOverrides },
		Check: enum(domain.SessionOverridesAllow, domain.SessionOverridesDeny),
	},
	field[int64]{
		Name: "MaxArtifactSize", Class: ClassGovernance, Conn: ConnRefusedGovernance,
		Get:   func(c domain.StoreConfig) int64 { return c.MaxArtifactSize },
		Check: nonNegative[int64]("MaxArtifactSize"),
	},

	// --- Class III: session ---
	field[domain.BlobStorage]{
		Name: "BlobStorage", Class: ClassSession, Conn: ConnOverlay,
		Get:   func(c domain.StoreConfig) domain.BlobStorage { return c.BlobStorage },
		Check: enum(domain.BlobStorageTarget, domain.BlobStorageInline),
	},
	field[domain.VerifyOnReadPolicy]{
		Name: "VerifyOnRead", Class: ClassSession, Conn: ConnOverlay,
		Get:   func(c domain.StoreConfig) domain.VerifyOnReadPolicy { return c.VerifyOnRead },
		Check: enum(domain.VerifyOnReadAuto, domain.VerifyOnReadForceEnabled, domain.VerifyOnReadDisabled),
	},
	field[int64]{
		Name: "InlineBlobLimit", Class: ClassSession, Conn: ConnOverlay,
		Get:   func(c domain.StoreConfig) int64 { return c.InlineBlobLimit },
		Check: maxVal[int64]("InlineBlobLimit", domain.MaxInlineBlobLimit),
	},
	field[domain.PackAlignmentPolicy]{
		Name: "PackAlignment", Class: ClassSession, Conn: ConnOverlay,
		Get:   func(c domain.StoreConfig) domain.PackAlignmentPolicy { return c.PackAlignment },
		Check: enum(domain.PackAlignmentAuto, domain.PackAlignmentNone, domain.PackAlignment512, domain.PackAlignment4096),
	},
	field[int64]{
		Name: "EagerFetchLimit", Class: ClassSession, Conn: ConnOverlay,
		Get: func(c domain.StoreConfig) int64 { return c.EagerFetchLimit },
		// No bound today; row carries class/conn for the overlay.
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

func (pipelineDesc) name() string       { return "Pipeline" }
func (pipelineDesc) class() FieldClass  { return ClassSession }
func (pipelineDesc) conn() ConnBehavior { return ConnDerived }

func (pipelineDesc) validate(domain.StoreConfig) error { return nil }

func (pipelineDesc) diverges(req, active domain.StoreConfig) (string, bool) {
	if len(req.Pipeline) == 0 || equalPipelines(req.Pipeline, active.Pipeline) {
		return "", false
	}
	return fmt.Sprintf("Pipeline: requested %v, active %v", req.Pipeline, active.Pipeline), true
}

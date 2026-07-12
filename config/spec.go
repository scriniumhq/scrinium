package config

// Field-spec registry (config review R-g; the seed of the Rules
// Engine, S-11). One row per domain.StoreConfig field — the single
// place that states a field's ADR-110 class and its fate at a
// connection. The scattered validators (ValidateImmutable /
// ValidateAgainstActive / divergentGovernance / divergentSession)
// remain the executable checks, but the CONFORMANCE TESTS in
// spec_test.go probe each of them against this table: a StoreConfig
// field without a spec row fails the completeness test, and a row
// whose class disagrees with actual PlanConnection behaviour fails the
// probe. "Forgot the field in one of the validators" is no longer a
// possible class of bug — it is a failing test.
//
// The table deliberately does NOT duplicate enum values, defaults or
// bounds: those live in ValidateImmutable/ApplyDefaults (and are
// per-field tested there); duplicating them here would create a second
// source of truth. The registry owns exactly the knowledge that used
// to be implicit and scattered — the classification.

// FieldClass is a field's ADR-110 class.
type FieldClass int

const (
	// ClassImmutable — class I: fixed at InitStore, changed only by
	// rebuilding the store.
	ClassImmutable FieldClass = iota + 1
	// ClassGovernance — class II: admin-mutable defaults, changed only
	// by an explicit admin act (UpdateConfig), versioned.
	ClassGovernance
	// ClassSession — class III: user-mutable session preferences,
	// self-describing per artifact; a connection may override them.
	ClassSession
)

// ConnBehavior is a populated, DIVERGING client field's fate at
// OpenStore (PlanConnection).
type ConnBehavior int

const (
	// ConnRefusedImmutable — ErrConfigMismatch.
	ConnRefusedImmutable ConnBehavior = iota + 1
	// ConnRefusedGovernance — ErrGovernanceMismatch.
	ConnRefusedGovernance
	// ConnOverlay — accepted as the connection's session overlay
	// (refused like governance under SessionOverrides=Deny).
	ConnOverlay
	// ConnIgnored — not compared at connection at all. The single
	// legitimate case is KDFParams: input-only at InitStore, owned by
	// the descriptor, never part of config comparison or snapshots.
	ConnIgnored
	// ConnDerived — not a free field: validated as a derivative of
	// class I (the Pipeline crypto-tail rule) rather than compared
	// verbatim. The non-crypto prefix behaves as ConnOverlay.
	ConnDerived
)

// FieldSpec is one row of the registry.
type FieldSpec struct {
	Name  string
	Class FieldClass
	Conn  ConnBehavior
}

// Specs is the registry: every domain.StoreConfig field, exactly once.
// Order follows the struct.
var Specs = []FieldSpec{
	{"PathTopology", ClassImmutable, ConnRefusedImmutable},
	{"BlobStorage", ClassSession, ConnOverlay},
	{"ManifestEncoding", ClassImmutable, ConnRefusedImmutable},
	{"ManifestCrypto", ClassImmutable, ConnRefusedImmutable},
	{"EncryptedDedup", ClassImmutable, ConnRefusedImmutable},
	{"PackAlignment", ClassSession, ConnOverlay},
	{"EagerFetchLimit", ClassSession, ConnOverlay},
	{"Pipeline", ClassSession, ConnDerived},
	{"ContentHasher", ClassImmutable, ConnRefusedImmutable},
	{"VerifyOnRead", ClassSession, ConnOverlay},
	{"SegmentSize", ClassImmutable, ConnRefusedImmutable},
	{"IdentityMode", ClassImmutable, ConnRefusedImmutable},
	{"DeletionPolicy", ClassGovernance, ConnRefusedGovernance},
	{"DeletionPolicyLock", ClassImmutable, ConnRefusedImmutable},
	{"RetentionPeriod", ClassGovernance, ConnRefusedGovernance},
	{"TombstoneGracePeriod", ClassGovernance, ConnRefusedGovernance},
	{"InlineBlobLimit", ClassSession, ConnOverlay},
	{"GCLeasePolicy", ClassGovernance, ConnRefusedGovernance},
	{"SessionOverrides", ClassGovernance, ConnRefusedGovernance},
	{"MaxArtifactSize", ClassGovernance, ConnRefusedGovernance},
	{"KDFParams", ClassImmutable, ConnIgnored},
}

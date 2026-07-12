package config

// Field classification vocabulary (ADR-110): the class and
// connection-behaviour enums that label every StoreConfig field. The
// field table itself — one declaration per field carrying these labels
// plus a typed getter and validator — lives in registry.go, and every
// per-field operation (ValidateImmutable, ValidateAgainstActive,
// divergentGovernance, divergentSession) is derived from it. These
// types are the shared vocabulary that table and its consumers speak.

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

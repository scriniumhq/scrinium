package errs

import "errors"

// Configuration: active-config resolution (max store.config seq, no pointer) and immutable-param
// validation. ErrInvalidConfig is produced by the Rules Engine.

// ErrConfigMissing — no store.config version has ever been written.
var ErrConfigMissing = errors.New("scrinium: config missing")

// ErrGovernanceMismatch — a CONNECTING client's config carries a
// class-II (governance) field diverging from the store's active
// defaults (ADR-110): DeletionPolicy, RetentionPeriod,
// TombstoneGracePeriod, GCLeasePolicy, SessionOverrides. Governance
// changes only by an explicit admin act (UpdateConfig), never by
// connecting with a softer config.
var ErrGovernanceMismatch = errors.New(
	"scrinium: governance config field (class II, ADR-110) differs from store defaults")

// ErrConfigMismatch — an attempt to change an immutable parameter
// through UpdateConfig, or a conflict between the cfg passed to
// OpenStore and the active store.config version, or an attempt to
// remove NoDelete while DeletionPolicyLock is in effect.
var ErrConfigMismatch = errors.New("scrinium: config mismatch")

// ErrInvalidConfig — a StoreConfig parameter is out of range or
// violates the Rules Engine.
var ErrInvalidConfig = errors.New("scrinium: invalid config")

// ErrInvalidTombstoneGracePeriod — TombstoneGracePeriod < 1h.
// A dedicated sentinel because this is the only param with runtime
// implications for multi-host safety.
var ErrInvalidTombstoneGracePeriod = errors.New("scrinium: invalid tombstone grace period")

// ErrInvalidKDFParams — KDFParams fail the minimum-validity check:
// Time < 1, Memory < 19456 KiB, Threads < 1.
var ErrInvalidKDFParams = errors.New("scrinium: invalid KDF params")

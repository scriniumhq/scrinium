package errs

import "errors"

// Configuration: active-config resolution (max store.config seq, no pointer) and immutable-param
// validation. ErrInvalidConfig is produced by the Rules Engine.

// ErrConfigMissing — no store.config version has ever been written.
var ErrConfigMissing = errors.New("scrinium: config missing")

// ErrConfigMismatch — an attempt to change an immutable parameter
// through UpdateConfig, or a conflict between the cfg passed to
// OpenStore and the active store.config version, or an attempt to
// remove NoDelete while
// DeletionPolicyLock is in effect.
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

package errs

import "errors"

// Configuration: system.config/current pointer and immutable-param
// validation. See docs/2. Internals/10 §10.1.4 for the pointer
// format and the four failure modes below; §4.4 for the Rules
// Engine that produces ErrInvalidConfig.

// ErrMissingConfigPointer — system.config/current is absent.
var ErrMissingConfigPointer = errors.New("scrinium: missing config pointer")

// ErrCorruptedConfigPointer — system.config/current exists but is
// invalid (empty, oversized, malformed ArtifactID).
var ErrCorruptedConfigPointer = errors.New("scrinium: corrupted config pointer")

// ErrDanglingConfigPointer — the pointer is syntactically valid
// but the artifact it references does not exist.
var ErrDanglingConfigPointer = errors.New("scrinium: dangling config pointer")

// ErrConfigMismatch — an attempt to change an immutable parameter
// through UpdateConfig, or a conflict between the cfg passed to
// OpenStore and the configuration loaded from
// system.config/current, or an attempt to remove NoDelete while
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

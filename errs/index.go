package errs

import "errors"

// StoreIndex and maintenance: schema migrations, integrity of the
// SQLite/Postgres backing store, the maintenance-agent lease,
// agent-specific preconditions (checkpoint availability, recovery
// kit).

// ErrIndexCorrupted — the StoreIndex is missing or its checksum
// does not match.
var ErrIndexCorrupted = errors.New("scrinium: index corrupted")

// ErrIndexSchemaMismatch — the StoreIndex schema version is
// incompatible with the running binary.
var ErrIndexSchemaMismatch = errors.New("scrinium: index schema mismatch")

// ErrMaintenanceInProgress — another Maintenance Agent holds the
// lease.
var ErrMaintenanceInProgress = errors.New("scrinium: maintenance in progress")

// ErrMetaKeyNotFound — the requested key is missing in store_meta.
var ErrMetaKeyNotFound = errors.New("scrinium: meta key not found")

// ErrNoCheckpoint — RebuildIndexAgent.Validate with
// Source: Checkpoint when no valid checkpoints are available.
var ErrNoCheckpoint = errors.New("scrinium: no valid checkpoint for Source=Checkpoint")

// ErrRecoveryKitRequired — RebuildIndexAgent.Validate with the
// Store in Corrupted after every descriptor replica has been lost
// and RecoveryKit is nil in the configuration.
var ErrRecoveryKitRequired = errors.New("scrinium: RecoveryKit required (descriptor lost, encrypted store)")

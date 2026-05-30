package errs

import (
	"errors"
	"io/fs"
)

// Store lifecycle: states (Bootstrapping/Locked/...) and the
// init/open transitions. See docs/2. Internals/01 §1.4 for the
// finite-state machine, docs/2. Internals/10 §10.1 for the open
// procedure that emits these.

// ErrStoreNotReady — the Store is in StateBootstrapping. The API
// is blocked until initialisation completes.
var ErrStoreNotReady = errors.New("scrinium: store not ready")

// ErrStoreNotFound — OpenStore: no store.json in the Location.
// Bridges to fs.ErrNotExist so a host can errors.Is(err,
// fs.ErrNotExist) when probing for an existing store. Distinct
// from ErrArtifactNotFound (an artifact inside an open Store).
var ErrStoreNotFound = newBridgedSentinel(
	"scrinium: store not found", fs.ErrNotExist,
)

// ErrStoreAlreadyExists — InitStore without WithForceReinit on top
// of an existing Store. Bridges to fs.ErrExist.
var ErrStoreAlreadyExists = newBridgedSentinel(
	"scrinium: store already exists", fs.ErrExist,
)

// ErrStoreCorrupted — every descriptor replica is corrupted, or
// the StoreIndex is corrupted. The Store is in StateCorrupted.
var ErrStoreCorrupted = errors.New("scrinium: store corrupted")

// ErrLocked — the operation was invoked in StateLocked on an
// encrypted store. Unlock is required. NOT used for closed stores
// — those return os.ErrClosed; conflating the two confused
// Plain-store users into searching for a passphrase.
var ErrLocked = errors.New("scrinium: store locked")

// ErrStoreReadOnly — MaintenanceModeReadOnly + a mutating operation.
// Bridges to fs.ErrPermission so generic "is this a permission
// problem?" checks at host layer return true.
var ErrStoreReadOnly = newBridgedSentinel(
	"scrinium: store read-only", fs.ErrPermission,
)

// ErrStoreOffline — MaintenanceModeOffline.
var ErrStoreOffline = errors.New("scrinium: store offline")

// ErrSharedIndexRequired — OpenStore on the SQLite backend has
// found a live foreign location.lock; a shared backend (Postgres)
// or a clean shutdown of the other process is required.
var ErrSharedIndexRequired = errors.New("scrinium: shared index required")

// ErrManifestsLost — RebuildIndexAgent did not find any manifests:
var ErrManifestsLost = errors.New("scrinium: manifests lost")

// ErrHostStorageLocked — Curator startup aborted: the WorkspaceDir
// of the transit buffer is held by another process (OS-level lock).
var ErrHostStorageLocked = errors.New("scrinium: host storage locked by another process")

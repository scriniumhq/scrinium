package errs

import "errors"

// Store lifecycle: states (Bootstrapping/Locked/...) and the
// init/open transitions. See docs/2. Internals/01 §1.4 for the
// finite-state machine, docs/2. Internals/10 §10.1 for the open
// procedure that emits these.

// ErrStoreNotReady — the Store is in StateBootstrapping. The API
// is blocked until initialisation completes.
var ErrStoreNotReady = errors.New("scrinium: store not ready")

// ErrStoreNotFound — OpenStore: no store.json in the Location.
// Distinct from ErrArtifactNotFound (an artifact inside an open
// Store).
var ErrStoreNotFound = errors.New("scrinium: store not found")

// ErrStoreAlreadyExists — InitStore without WithForceReinit on top
// of an existing Store.
var ErrStoreAlreadyExists = errors.New("scrinium: store already exists")

// ErrStoreCorrupted — every descriptor replica is corrupted, or
// the StoreIndex is corrupted. The Store is in StateCorrupted.
var ErrStoreCorrupted = errors.New("scrinium: store corrupted")

// ErrLocked — the operation was invoked in StateLocked. Unlock is
// required.
var ErrLocked = errors.New("scrinium: store locked")

// ErrStoreReadOnly — MaintenanceModeReadOnly + a mutating operation.
var ErrStoreReadOnly = errors.New("scrinium: store read-only")

// ErrStoreOffline — MaintenanceModeOffline.
var ErrStoreOffline = errors.New("scrinium: store offline")

// ErrSharedIndexRequired — OpenStore on the SQLite backend has
// found a live foreign location.lock; a shared backend (Postgres)
// or a clean shutdown of the other process is required.
var ErrSharedIndexRequired = errors.New("scrinium: shared index required")

// ErrManifestsLost — RebuildIndexAgent did not find any manifests:
// ManifestStorage: Local with the local disk lost and no
// HostStorage available.
var ErrManifestsLost = errors.New("scrinium: manifests lost")

// ErrHostStorageLocked — Curator startup aborted: the WorkspaceDir
// of the transit buffer is held by another process (OS-level lock).
var ErrHostStorageLocked = errors.New("scrinium: host storage locked by another process")

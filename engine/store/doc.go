// Package store is the Scrinium Storage Engine (layer L2).
//
// A self-contained CAS engine: it accepts Artifacts, runs them through a
// configurable Pipeline, places them on a backend through a Driver, and
// keeps accounting in a StoreIndex. It operates on cryptographic
// identifiers (ContentHash, BlobRef, ArtifactID). The two metadata blocks
// (Ext for engine custom indexes like vfsmeta, Usr for opaque host data) are
// passed through but never interpreted.
//
// # Contract
//
// The Store contract is split into three interfaces, declared in store.go
// — read it first; it is the package's "header":
//
//   - DataStore  — operations on artifacts (Put, Get, Delete, Walk, …),
//     the surface seen by client code and decorators.
//   - AdminStore — administrative API (Unlock, RotateKEK, UpdateConfig,
//     Close, System), the surface seen by the Store's owner.
//   - Store      — the union of the two, returned by InitStore and OpenStore.
//
// The concrete type is the unexported *store; it is never exported.
// Behaviour is split across the sibling files mapped below, grouped by the
// concern each serves.
//
// # Concurrency
//
// A live *store is guarded by three mutexes with a fixed lock order
// (crypto.mu → stateMu → cfgMu). The full model — what each guards, the
// acquire/release discipline per call path, and the invariants a refactor
// must preserve — lives in store_impl.go's header. Read it before touching
// any locking.
//
// This package is the canonical (foundation) instance of the system-wide
// concurrency model; the normative description and cross-layer invariants
// live in docs/2 Internals/13 Concurrency Model.md.
//
// # Reading order
//
// store.go (contracts) → store_impl.go (the type and lock order) →
// access.go and admin_state.go (the entry gates every method funnels
// through) → the data_* plane → the admin_* plane and crypto_state.go →
// the lifecycle_* constructors → the system_* plumbing → internal/.
//
// # File map
//
// Contracts and core type:
//
//   - store.go        — the Store / DataStore / AdminStore / SystemStore
//     interfaces and the SystemArtifact value type.
//   - readhandle.go   — the ReadHandle interface.
//   - store_impl.go   — the *store struct, its fields, the lock-order
//     invariant, and System().
//   - options.go      — StoreOption constructors (With…) and the
//     PassphraseProvider / PassphraseHint contract.
//   - doc.go          — this map.
//
// Entry gates and state machine:
//
//   - access.go       — the entry preamble shared by every method.
//   - admin_state.go  — State, Capabilities, SetMaintenanceMode, and the
//     priority-of-checks operational gate.
//
// Data plane (DataStore):
//
//   - data_put.go          — Put orchestrator and write-path policy; the
//     physical mechanics live in internal/casio.
//   - data_get.go          — Get, read-handle dispatch, manifest loading.
//     The ReadHandle implementations live in internal/casio.
//   - data_delete.go       — Delete.
//   - data_verify.go       — Verify and the VerifyOnRead policy.
//   - data_walk.go         — Walk.
//   - data_capacity.go     — Capacity.
//   - data_rollback.go     — RollbackSession.
//   - data_pipeline.go     — the store↔pipeline glue (pipelineRunner).
//
// Admin plane (AdminStore):
//
//   - admin_config.go      — Config, UpdateConfig, ConfigHistory.
//   - admin_crypto.go — Unlock, RotateKEK, SetPassphrase, and
//     ExportRecoveryKit; holds crypto.mu for the whole operation. The
//     pure mechanics (CallProvider, BuildRecoveryKit, InitEncryptedDEK)
//     live in internal/crypto.
//   - admin_close.go       — Close.
//   - crypto material lives in internal/crypto (crypto.State): DEK,
//     provider, and resolver under crypto.mu.
//
// Lifecycle and bootstrap:
//
//   - lifecycle_init.go      — InitStore.
//   - lifecycle_open.go      — OpenStore.
//   - lifecycle_construct.go — buildStore, unlockBootstrap, and replica
//     healing, the tail shared by both constructors.
//
// System and config plumbing:
//
//   - systemstore.go    — the systemStore facade (Put/Get/Delete/Walk),
//     a thin adapter over namedio. System artifacts are
//     addressed by name in their own system/ address space and are never
//     indexed (ADR-85), so there is no pointer file and no opt-out flag.
//
// internal/ subpackages — concerns that own their state and so are
// separate packages (the boundary along which the engine can be split):
//
//   - casio    — the artifact I/O mechanics over the engine/artifact
//     format: blob materialization, manifest assembly/persistence (write)
//     and manifest load, blob open, and verification (read).
//   - descriptor   — the on-disk descriptor and its L2 cache.
//   - keyring      — the KDF (Argon2id) and KEK/DEK wrap/unwrap kernels.
//   - namedio — the system/<name>/<seq> address-space mechanics
//     (name validation, seq claim via atomic create, inline-manifest
//     build, verify-on-read) shared by the systemStore facade and the
//     storeconfig bootstrap path (ADR-85).
//   - storeconfig  — the StoreConfig format, defaults, validation, and
//     persistence (over namedio).
//   - orphanscan   — bootstrap-time orphan recovery.
//   - reconcile    — replica reconciliation.
//   - recoverykit  — the recovery-kit format.
//
// # DAG
//
// store imports domain, driver, event, index, and pipeline (plus the
// engine/internal and store/internal helper packages). It does not import
// agent or projection.
package store

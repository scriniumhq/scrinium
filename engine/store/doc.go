// Package store is the Scrinium Storage Engine (layer L2).
//
// A self-contained CAS engine: it accepts Artifacts, runs them through
// a configurable Pipeline, places them on a backend through a Driver,
// and keeps accounting in a StoreIndex. It operates on cryptographic
// identifiers (ContentHash, BlobRef, ArtifactID). The two metadata
// blocks per ADR-54 (Ext for engine extensions like fsmeta, Usr for
// opaque host data) are passed through but never interpreted.
//
// # Contract
//
// The Store contract is split into three interfaces, all declared in
// coreapi — read coreapi first; it is the package's "header":
//
//   - DataStore  — operations on artifacts (Put, Get, Delete, Walk, …).
//     The surface seen by client code, decorators, and Curator.
//   - AdminStore — administrative API (Unlock, RotateKEK, UpdateConfig,
//     Close, System). The surface seen by the Store's owner.
//   - Store      — the union of the two. Returned by InitStore and
//     OpenStore.
//
// The concrete type is the unexported *store; it is never exported.
// Behaviour is split across the sibling files mapped below, grouped by
// the concern each serves.
//
// # Concurrency
//
// A live *store is guarded by three mutexes with a fixed lock order
// (crypto.mu → stateMu → cfgMu). The full model — what each guards, the
// acquire/release discipline per call path, and the invariants a
// refactor must preserve — is documented in store_impl.go's header and in
// docs Internals "Store Concurrency Model". Read it before touching any
// locking.
//
// # Reading order
//
// coreapi (contracts) → store_impl.go (the type + lock order) → access.go
// and admin_state.go (the entry gates every method funnels through) →
// the data_* plane → the admin_* plane and crypto_state.go → the
// lifecycle_* constructors → the system/config plumbing → the
// internal/ subpackages.
//
// # File map
//
// Core type and wiring:
//
//   - store_impl.go        — the *store struct, its fields, the lock-order
//     invariant, and System().
//   - doc.go          — this map.
//   - options.go      — StoreOption constructors (With…).
//   - events.go       — the publish helper.
//
// Entry gates and state machine:
//
//   - access.go       — enterRead / enterWrite / enterAdmin, the entry
//     preamble shared by every method.
//   - admin_state.go  — State, Capabilities, SetMaintenanceMode, and
//     checkOperational (the priority-of-checks gate).
//
// Data plane (DataStore):
//
//   - data_put.go          — Put orchestrator plus write-path policy
//     (input validation, feature gates, write-key resolution, DEK
//     custody). The physical mechanics live in internal/artifactwriter.
//   - data_get.go          — Get, read-handle dispatch, loadManifest.
//   - data_read_handles.go — the ReadHandle implementations (inline,
//     target, verifying).
//   - data_delete.go       — Delete.
//   - data_verify.go       — Verify and the VerifyOnRead policy.
//   - data_walk.go         — Walk.
//   - data_capacity.go     — Capacity.
//   - data_rollback.go     — RollbackSession.
//   - data_pipeline.go     — the store↔pipeline glue (pipelineRunner)
//     and the errPipelineWithInline policy.
//
// Admin plane (AdminStore):
//
//   - admin_config.go          — Config, UpdateConfig, ConfigHistory.
//   - admin_crypto.go          — Unlock / RotateKEK / SetPassphrase /
//     ExportRecoveryKit, thin delegators to the bodies below.
//   - admin_crypto_impl.go     — the multi-step crypto bodies and
//     commitDescriptor; holds crypto.mu for the whole operation.
//   - admin_crypto_resolver.go — callProvider, the PassphraseProvider
//     glue.
//   - admin_close.go           — Close.
//   - crypto_state.go          — the cryptoState component: DEK,
//     descriptor, provider, and resolver under crypto.mu.
//
// Lifecycle and bootstrap:
//
//   - lifecycle_init.go      — InitStore.
//   - lifecycle_open.go      — OpenStore.
//   - lifecycle_construct.go — buildStore and unlockBootstrap, the tail
//     shared by both constructors.
//   - bootstrap_dek.go       — initEncryptedDEK and buildRecoveryKit.
//   - bootstrap_replicas.go  — replica healing.
//
// System and config plumbing:
//
//   - system_write.go    — writeInlineSystemArtifact(Unindexed), the
//     inline system-artifact write primitive.
//   - system_options.go  — SystemPutOption (WithoutIndex).
//   - config_writer.go   — the configWriter closure bound to the
//     primitive above.
//
// internal/ subpackages — concerns that own their state and so are
// separate packages (the boundary along which the engine can be split;
// see the concurrency model's "Implication for refactoring"):
//
//   - artifactwriter   — the artifact write-path mechanics: blob
//     materialization, manifest assembly, and persistence.
//   - descriptor   — the on-disk descriptor and its L2 cache.
//   - keyring      — the KDF (Argon2id) and KEK/DEK wrap/unwrap kernels.
//   - storeconfig  — the StoreConfig format, defaults, validation, and
//     persistence.
//   - systemstore  — the SystemStore facade over system.config and
//     system.state.
//   - orphanscan   — bootstrap-time orphan recovery.
//   - reconcile    — replica reconciliation.
//   - recoverykit  — the recovery-kit format.
//
// # DAG
//
// store imports event and driver. It does not import plugin, index,
// curator, agent, maintenance, or projection.
package store

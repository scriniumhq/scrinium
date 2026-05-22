// Package descriptor reads and writes the Store descriptor file
// (store.json). Per §10.1.3 the descriptor carries Store identity
// and the cryptographic Paranoid: StoreID, schema_version, sequence,
// DEK, dek_encrypted, kdf_params.
//
// Projection parameters (PathTopology, ManifestStorage, etc.) live
// in the system.config artifact pointed to by system.config/current
// (§10.1.4) — not here. The descriptor is silent about them.
//
// Format: JSON, pretty-printed, with a trailing newline. The file
// format is engine-private and may evolve through migrations behind
// schema_version.
//
// DAG: descriptor depends on driver.Driver, errs (sentinel
// errors), and stdlib. No imports from core, domain, or any
// consumer package.
//
// cache owns the L2 cached projection of the
// on-disk descriptor (§10.1.5). The cache is a fast-start aid: with
// it OpenStore can verify that the on-disk descriptor matches what
// the previous session saw without re-parsing it. It is never
// authoritative — a missing or corrupt cache is always recoverable
// by re-reading the L0/L1 descriptor replicas.
//
// Split out of engine/core so the cache's persistence and
// consistency logic lives in one home rather than half in
// descriptor_cache.go and half in lifecycle.go (Refresh used to sit
// in lifecycle.go, unrelated to the bootstrap/DEK concerns there).
//
// The package depends only on a narrow MetaStore (Get/SetMeta over
// store_meta) and on core/internal/descriptor; it never touches
// *store. core's StoreIndex satisfies MetaStore implicitly.
package descriptor

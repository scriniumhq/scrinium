// Package descriptorcache owns the L2 cached projection of the
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
package descriptorcache

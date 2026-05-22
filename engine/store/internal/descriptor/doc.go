// Package descriptor reads, writes, and (de)serialises the Store
// descriptor file: Store identity (StoreID, schema_version, sequence)
// and the crypto material (DEK, dek_encrypted, kdf_params). Projection
// parameters live in system.config, not here.
//
// The descriptor is stored as two byte-identical replicas, L0
// (store.json) and L1 (.store.backup.json), written together by
// Persist. The L2 cache (Cache, in cache.go) is a fast-start
// projection in store_meta, never authoritative.
//
// Replica reading and the heal/split-brain decision algorithm live in
// the reconcile subpackage. This package owns the descriptor's shape,
// (de)serialisation, checksum, equality, and the two-replica write;
// reconcile owns the recovery decision over them.
//
// Depends only on driver.Driver, errs, and a narrow MetaStore. No
// imports from coreapi, domain, or any consumer package.
package descriptor

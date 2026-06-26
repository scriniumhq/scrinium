// Package descriptor reads, writes, and (de)serialises the Store
// descriptor file: Store identity (StoreID, schema_version, sequence)
// and the crypto material (DEK, dek_encrypted, kdf_params). Projection
// parameters live in system.config, not here.
//
// The descriptor is stored as two byte-identical replicas, L0
// (store.json) and L1 (.store.backup.json), written together by
// Persist.
//
// Replica reading and the heal/split-brain decision algorithm live in
// the reconcile subpackage. This package owns the descriptor's shape,
// (de)serialisation, checksum, equality, and the two-replica write;
// reconcile owns the recovery decision over them.
//
// Depends only on driver.Driver and errs. No imports from store,
// domain, or any consumer package.
package descriptor

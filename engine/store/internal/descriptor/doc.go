// Package descriptor reads, writes, and (de)serialises the Store
// descriptor: Store identity (StoreID, schema_version, sequence)
// and the crypto material (DEK, dek_encrypted, kdf_params). Projection
// parameters live in store.config, not here.
//
// The descriptor is stored as two byte-identical keep=0 named-cell
// replicas — store.descriptor (Name) and store.descriptor.backup
// (BackupName) — written together by WriteBoth (ADR-103), NOT as
// store.json/.store.backup.json files in the location root.
//
// Replica reading and the heal/split-brain decision algorithm live in
// the reconcile subpackage. This package owns the descriptor's shape,
// (de)serialisation, equality, and the two-replica write; reconcile owns
// the recovery decision over them. Replica integrity is verify-on-read of
// the named cell's content_hash (canonical sha256, named layer) — there is
// no separate descriptor checksum (ADR-103, 2026-06-27).
//
// Depends on driver.Driver, the named layer, domain, and hashing. No
// imports from store or any consumer package.
package descriptor

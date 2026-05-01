// Package descriptor reads and writes the Store descriptor file
// (store.json). Per §10.1.3 the descriptor carries Store identity
// and the cryptographic envelope: StoreID, schema_version, sequence,
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
package descriptor

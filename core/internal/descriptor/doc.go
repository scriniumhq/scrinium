// Package descriptor reads and writes the Store descriptor file
// (store.json). The descriptor is the source of truth for Store
// identity and immutable parameters: it is created at InitStore,
// read at every OpenStore, never silently rewritten.
//
// The descriptor is intentionally minimal — it carries identity
// (StoreID, format version) and a snapshot of immutable
// StoreConfig fields that the engine validates against the active
// StoreConfig at OpenStore time. Mutable parameters live in the
// StoreConfig artifact pointed to by system.config/current; they
// do NOT live here, and the descriptor is silent about them.
//
// Format: JSON, pretty-printed, with a trailing newline. Designed
// to be diagnosable by hand: open store.json in any editor, the
// fields are self-explanatory.
//
// This is an internal package: the package path enforces that
// only core/ (and its subpackages) can import it. The descriptor
// shape is engine-private and may evolve through migrations
// behind the FormatVersion field; the file format is not part of
// the public API.
//
// DAG: descriptor imports nothing from the project except core's
// type aliases through composition (callers pass core types in,
// the descriptor returns its own types out, and core converts).
// We avoid importing core directly here to keep this package
// importable from anywhere within core/.
package descriptor

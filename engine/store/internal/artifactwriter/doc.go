// Package artifactwriter holds the physical write path for an artifact:
// turning a payload into a committed blob (hash, pipeline, staging,
// dedup, commit), assembling the manifest, and persisting both through
// the Driver and StoreIndex.
//
// It is the stateless mechanics half of store.Put. Everything that
// touches the engine's mutable shared state — the state-machine gate,
// the active-config snapshot, the crypto lock and DEK lifetime — stays
// in the store package. artifactwriter receives what it needs as plain
// arguments: a StoreConfig snapshot, the resolved write KeyID, and (for
// an encrypting manifest) a transient DEK copy the caller owns and
// wipes. No store mutex is reachable from here, by construction.
//
// Dependencies (Driver, StoreIndex, HashRegistry, TransformerRegistry)
// are injected once via New, mirroring the systemstore facade. They are
// set at construction and never mutated, so a Writer is safe to build
// per operation or hold as a field.
package artifactwriter

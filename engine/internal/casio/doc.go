// Package casio is the store-side write orchestration for artifacts:
// the entity-aware half that the pure engine/artifact format library
// deliberately omits. It turns a payload into a committed blob and an
// indexed manifest by combining the format (engine/artifact: paths, ID
// computation, manifest encoding) with the three entities artifact does
// not touch — the Driver (staging, rename, manifest write), the
// StoreIndex (dedup probe, IndexManifest, Resolve), and the Pipeline
// (forward transform on write).
//
// Layering: casio ← store, casio → {artifact, driver, index,
// pipeline, domain}. It never reaches into *store internals; the store
// injects its dependencies through New, exactly as systemstore does. The
// pure format stays in engine/artifact; casio adds the I/O around it.
//
// The write path is deliberately three phases (Materialize →
// AssembleManifest → PersistManifest) rather than one call: store.Put
// borrows the DEK under its crypto lock only for the AssembleManifest
// step (ComputeArtifactID needs it) and wipes it immediately, so the lock
// scope stays minimal. Collapsing the phases would force the DEK to be
// held across blob I/O.
package casio

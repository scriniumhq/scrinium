// Package multistore wraps multiple store.Store instances behind a
// single store.DataStore surface: write-side routing, read-side
// fallback, optional cross-store dedup, and the global
// MultistoreIndex. Per ADR-53 this lives in engine/wrapper/ — a
// peer of bundler/ and chunker/, not a sub-package of curator/ —
// because Curator's standalone-service role is orthogonal to the
// wrapping logic.
//
// Implementation lands with chunk S1; for now the package only
// holds contracts and value types the rest of the engine builds
// against (WrapperFactory, WrapperDeps, policy enums, routing
// types).
package multistore

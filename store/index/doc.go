// Package index contains implementations of store.StoreIndex and
// curator.MultistoreIndex.
//
// Subpackages:
//   - index/sqlite — the primary StoreIndex implementation for the
//     embedded mode. (M1.2)
//   - index/postgres — a shared StoreIndex for multi-host deployments.
//     Lands when needed.
//   - index/memory — an in-memory implementation for tests.
//   - index/multistore — a curator.MultistoreIndex implementation.
//     (M4.1)
//
// In M0 this package contains only a stub doc and the shared
// types/options used across implementations.
//
// DAG: index imports core (the StoreIndex contract), driver (for
// access to Capabilities when picking optimisations), and event
// (for emitting metric events).
package index

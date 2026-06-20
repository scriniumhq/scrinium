// Package index defines the StoreIndex contract (index.StoreIndex) and
// hosts its implementations. The MultistoreIndex contract and its
// implementation live in engine/wrapper/multistore.
//
// Subpackages:
//   - index/sqlite — the primary StoreIndex implementation for the
//     embedded mode. (M1.2)
//   - index/postgres — a shared StoreIndex for multi-host deployments.
//     Lands when needed.
//
// Besides the contract, this package holds the shared types and
// options used across implementations.
//
// DAG: index defines the StoreIndex contract and imports domain and
// event (for emitting metric events). Implementations that need driver
// Capabilities (e.g. index/sqlite) import driver themselves.
package index

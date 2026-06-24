// Package index defines the StoreIndex contract (index.StoreIndex) and
// hosts its implementations. The MultistoreIndex contract lives in
// engine/wrapper/multistore (ADR-91/43); its implementation there is
// currently an architectural skeleton (stubbed bodies).
//
// Subpackages:
//   - index/sqlite — the primary StoreIndex implementation for the
//     embedded mode. (M1.2)
//   - index/postgres — a shared StoreIndex for multi-host deployments.
//     Lands when needed.
//   - index/memory — an in-memory implementation for tests.
//
// DAG: index defines the StoreIndex contract and imports domain and
// event (for emitting metric events). Implementations that need driver
// Capabilities (e.g. index/sqlite) import driver themselves.
package index

// Package sqlite is the reference implementation of index.StoreIndex
// backed by SQLite.
//
// Two concrete SQLite drivers are supported via build tags. Without
// any tag the package uses modernc.org/sqlite — a pure-Go
// implementation that requires no C toolchain and cross-compiles
// trivially. The build tag `sqlite_cgo` selects mattn/go-sqlite3,
// which links against the system SQLite library through cgo and is
// noticeably faster on write-heavy workloads. The choice happens at
// build time; the package's public API is identical either way.
//
// File-based and in-memory databases are both supported. Pass
// ":memory:" as the path to Open to create a private in-memory
// database; this is the recommended setup for unit tests of higher
// layers.
//
// Concurrency:
//   - All Index methods are safe for concurrent use.
//   - Mutating methods open and commit their own transactions
//     internally; the caller never drives transactions explicitly.
//   - Concurrent writers are coordinated by SQLite via busy_timeout
//     (default 5s); a contention that exceeds the timeout is
//     reported as errs.ErrLeaseHeld with a wrapped sqlite busy
//     error and emitted as the index.contention_error event.
//
// Schema migrations are applied automatically on Open. Forward-only:
// downgrades are not supported. Schema versions live in the
// schema_version table; a mismatch between the embedded current
// version and the on-disk version returns errs.ErrIndexSchemaMismatch.
//
// DAG: this package imports index (the StoreIndex contract), driver
// (capabilities — read-only), and event (metric events). It does
// not import agent or higher layers.
package sqlite

// Package projectionfx supplies test fixtures for the projection
// package and its consumers (FSOps, FUSE/WebDAV daemons,
// ingester). The two main types are:
//
//   - FakeSource — an in-memory ProjectionSource that holds a slice
//     of manifests and parallel payload bytes. Walk delivers
//     manifests in insertion order; Get returns a ReadHandle over
//     the registered payload.
//
//   - FakeReadHandle — a core.ReadHandle backed by bytes.Reader.
//     Configurable through options (default: random access enabled).
//
// Both are deliberately minimal: tests that need richer behaviour
// (errors injected on the Nth call, Walk that respects ctx
// cancellation, etc.) extend the type or build a different fake.
package projectionfx

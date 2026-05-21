// Package systemstore implements coreapi.SystemStore — the
// engine-internal API for service artifacts (configuration history,
// agent cursors, index snapshots, lease coordination), addressed by
// name through per-name pointer files (ADR-57).
//
// Extracted from the store god-package: the implementation depends
// only on coreapi/domain/driver and two injected primitives from the
// store layer (artifact write + inline read-handle construction), so
// it carries none of *store's private state. The store wires it once
// at construction via New and exposes it through Store.System().
//
// Atomicity model (per-name Put):
//
//  1. Read the current pointer for name (may be absent).
//  2. Write the new artifact through the injected writer.
//  3. Atomically replace the pointer file (driver.Put is atomic).
//  4. If a predecessor existed, delete its manifest file.
//
// Crash between (3) and (4) leaves the predecessor manifest as an
// orphan with no pointer; the bootstrap Orphan Scan (docs/2 §10.2.3)
// sweeps these. Crash between (2) and (3) leaves the new manifest as
// an orphan; same recovery.
package systemstore

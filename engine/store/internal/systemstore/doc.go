// Package systemstore implements store.SystemStore — the
// engine-internal API for service artifacts (configuration history,
// agent cursors, index snapshots, lease coordination), addressed by
// name through per-name pointer files (ADR-57).
//
// It depends only on store/domain/driver plus two primitives
// injected by the store layer (artifact write + inline read-handle
// construction), so it carries none of *store's private state. The
// store wires it once at construction via New and exposes it through
// Store.System().
//
// Atomicity model (per-name Put):
//
//  1. Read the current pointer for name (may be absent).
//  2. Write the new artifact through the injected writer.
//  3. Atomically replace the pointer file (driver.Put is atomic).
//  4. If a predecessor existed, delete its manifest file.
//
// A crash between (3) and (4) leaves the predecessor manifest as an
// orphan with no pointer; the bootstrap Orphan Scan reclaims it. The
// pointer swap in (3) is the linearisation point — a reader either
// sees the old artifact or the new one, never a torn state.
package systemstore

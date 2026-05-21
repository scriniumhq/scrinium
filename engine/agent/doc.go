// Package agent contains the contracts and implementations of
// background agents: Ingester, GC, Scrub, Snapshot, Sync, Ejector.
// A coreutils-style toolkit for the storage: it automates the
// maintenance work without forcing the host application to
// hand-roll its own logic.
//
// Two lifecycle modalities:
//   - BackgroundAgent — cyclic or continuous work. Implemented here.
//   - MaintenanceAgent — one-shot operation. The contract lives in
//     core (see store.MaintenanceAgent); the implementations live in
//     maintenance/.
//
// Two ownership modalities:
//   - Curator-managed (Scrub, Snapshot) — automatically launched by
//     Curator for every registered Target.
//   - User-managed (Ingester, GC, Ejector) — created and started by
//     the host application explicitly through the package
//     constructors.
//
// DAG: agent imports core, driver, event. It does not import
// curator, maintenance, or projection.
//
// Implementations land in M3 (GC, Scrub, Snapshot, RebuildIndex)
// and M6 (Ingester, Ejector). In M0 — contracts and configuration
// types.
package agent

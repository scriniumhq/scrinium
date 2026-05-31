// Package agent contains the contracts and implementations of
// background agents: Ingester, GC, Scrub, Snapshot, Sync, Ejector.
// A coreutils-style toolkit for the storage: it automates the
// maintenance work without forcing the host application to
// hand-roll its own logic.
//
// Two lifecycle modalities:
//   - BackgroundAgent — cyclic or continuous work. Implemented here.
//   - MaintenanceAgent — one-shot operation. The contract lives in
//     domain (see domain.MaintenanceAgent); the implementations live in
//     maintenance/.
//
// Two ownership modalities:
//   - Engine-managed (Scrub, Snapshot) — automatically launched
//     for every registered Target.
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
// Package maintenance contains the implementations of one-shot
// administrative agents: RebuildIndexAgent, MigrateIndexAgent,
// VerificationAgent, MoveStoreAgent.
//
// Each one implements domain.MaintenanceAgent (see domain/agent.go).
// They are launched strictly explicitly (CLI/API), never
// automatically. Exclusivity is guaranteed through a lease in
// system.state/maintenance/lease.
//
// Stable in chunk A1 (M3): RebuildIndexAgent.
// Reserved (stabilised on demand): MigrateIndexAgent,
// VerificationAgent, MoveStoreAgent.
//
// DAG: maintenance imports domain, store, driver, event. It does not import
// curator, agent, or projection.
package agent

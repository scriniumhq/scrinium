// Package maintenance contains the implementations of one-shot
// administrative agents: RebuildIndexAgent, MigrateIndexAgent,
// VerificationAgent, MoveStoreAgent.
//
// Each one implements core.MaintenanceAgent (see core/plugins.go).
// They are launched strictly explicitly (CLI/API), never
// automatically. Exclusivity is guaranteed through a lease in
// system.state/maintenance/lease.
//
// Stable in M3.4: RebuildIndexAgent.
// Reserved (stabilised on demand): MigrateIndexAgent,
// VerificationAgent, MoveStoreAgent.
//
// DAG: maintenance imports core, driver, event. It does not import
// curator, agent, or projection.
package maintenance

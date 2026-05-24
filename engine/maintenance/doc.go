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
package maintenance

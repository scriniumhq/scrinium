// Package migration is the Migration Agent (3. Reference/06 Agents/08
// Migration): the bundler's paired background worker. It drains the
// migration queue (system.state/migration/pending), packs small blobs
// accumulated in the transit store into volumes (container manifest + TOC
// blob + body blob) and delivers the result to the destination store. It
// appears in the stack only when a Target is wrapped by the bundler.
//
// Wrapper (synchronous fill) ↔ agent (finalize) communicate through state
// on the medium (the queue and the transit store), not direct calls.
//
// STATUS: architectural skeleton. The contract (MigrationConfig,
// MigrationStats, MigrationAgent, registration as agent kind "migration")
// matches the doc. The cycle (lease, read queue, group, pack, deliver,
// prune) is a stub (errs.ErrNotImplemented / TODO).
//
// NOT the same as MigrateIndexAgent (index schema/backend migration) or
// namespace migration — three distinct "migration" operations, no shared
// code (08 Migration §8.9.1, ADR-96).
package migration

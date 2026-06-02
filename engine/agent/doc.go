// Package agent contains the contract and implementations of
// Scrinium agents: Ingester, GC, Scrub, Snapshot, Rebuild, Ejector,
// and the reserved Sync, Migration, and MoveStore. A coreutils-style
// toolkit for the storage: it automates maintenance work without
// forcing the host application to hand-roll its own logic.
//
// One modality (ADR-68). An agent is a one-shot procedure over a
// Store — Validate then Run, returning a domain.AgentResult —
// initiated outside the operation path. There is no separate
// background-versus-maintenance split: "background" is an external way
// of invoking an agent (a scheduler, a bus subscriber, or a manual
// call), not a property of the agent. Agents keep no resident
// in-memory state; progress lives in the Store (last_verified_at,
// orphan selection, queues), so an interrupted Run resumes from where
// it left off on the next call.
//
// Construction is through the registry (ADR-51), like engine/wrapper:
// agent.Register installs a Factory in an init(), and the assembler
// looks it up by kind and calls Factory.Build(store, cfg, deps). The
// dependencies (Publisher, Driver, Index, HostID) arrive in AgentDeps
// from the assembler, so the Store facade is never opened up.
//
// Status: GC, Scrub, Snapshot are implemented; Rebuild is nearly
// complete (one path is a stub); Ingester and Ejector are stubs
// pending M6. Sync, Migration, and MoveStore are reserved.
//
// DAG: agent imports domain, store, driver, event. It does not import
// projection.
package agent

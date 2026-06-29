// Package migration is the Migration Agent (3. Reference/06 Agents/08
// Migration): the bundler's paired background worker. It drains the
// migration queue (extension.bundler.migration.pending), packs small blobs
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

import (
	"context"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
)

// MigrationConfig configures the migration agent (08 Migration §8.2).
type MigrationConfig struct {
	ScanInterval time.Duration // base queue-scan period; default 1h
	BatchSize    int           // queue items per pass; default 200
	LeaseTTL     time.Duration // coordination lease TTL; default 5m
}

// MigrationStats is the per-run business result (08 Migration §8.3).
type MigrationStats struct {
	PacksSealed     int64
	ArtifactsPacked int64
	ArtifactsMoved  int64 // delivered to a dest other than the accumulation site
	SkippedDeleted  int64 // ref_count == 0 at seal time
	BytesMoved      int64
}

// MigrationAgent is the agent contract plus an off-schedule trigger.
type MigrationAgent interface {
	agent.Agent

	// RunNow forces finalization for the given dest outside the schedule
	// (e.g. an external "ship what's accumulated now" signal). A plain Run
	// is one queue pass; periodicity is provided by the scheduler.
	RunNow(ctx context.Context, dest string) error
}

type migrationAgent struct {
	agent.BaseState
	st   store.Store
	deps agent.AgentDeps
	cfg  MigrationConfig
}

func (a *migrationAgent) AgentType() string { return "migration" }

func (a *migrationAgent) Validate(ctx context.Context) error {
	// TODO(migration): check store mode, bundler presence, lease absence.
	return ctx.Err()
}

func (a *migrationAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	// TODO(migration): one pass per §8.4 — acquire lease, read queue
	// (BatchSize), group by Finalize policy, pack ref_count>0 blobs into a
	// volume, deliver to dest (commit dest index THEN prune transit), emit
	// EventPackSealed / EventArtifactMigrated.
	return nil, errs.ErrNotImplemented
}

func (a *migrationAgent) RunNow(ctx context.Context, dest string) error {
	// TODO(migration): force finalize for dest off-schedule.
	return errs.ErrNotImplemented
}

type migrationFactory struct{}

func (migrationFactory) Name() string { return "migration" }

func (migrationFactory) Build(st store.Store, cfg any, deps agent.AgentDeps) (agent.Agent, error) {
	c, _ := cfg.(MigrationConfig) // nil/zero cfg → defaults (TODO: apply §8.2 defaults)
	return &migrationAgent{
		BaseState: agent.NewBaseState(deps.Logger),
		st:        st,
		deps:      deps,
		cfg:       c,
	}, nil
}

// init registers the migration agent factory so a blank import wires it in
// (ADR-68 SPI). Unlike the core agents (preset), migration is the bundler's
// paired agent: it is declared only when bundling is configured — wiring it
// into the stack via the bundler Extension's Agents() is a later step.
func init() { agent.Register(migrationFactory{}) }

// Guard.
var _ MigrationAgent = (*migrationAgent)(nil)

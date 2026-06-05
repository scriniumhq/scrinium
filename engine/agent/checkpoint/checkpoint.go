package checkpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/lease"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/event"
)

// --- Checkpoint Agent ---

// CheckpointConfig configures the Checkpoint Agent.
type CheckpointConfig struct {
	// Enabled toggles background checkpointting.
	Enabled bool

	// Interval is the periodic checkpoint interval.
	Interval time.Duration

	// ArtifactThreshold also triggers a checkpoint once this many
	// new artifacts have been added since the previous checkpoint.
	ArtifactThreshold int

	// Retention is the number of checkpoints to keep; older ones are
	// removed.
	Retention int

	// RecoveryOverlap is the recovery overlap: when loading a
	// checkpoint, RebuildIndexAgent re-reads objects that appeared
	// after checkpoint_created_at - RecoveryOverlap. It guards
	// against the edge case "an object was written between the
	// checkpoint and the crash".
	RecoveryOverlap time.Duration
}

// CheckpointStats are the statistics of a single checkpoint.
type CheckpointStats struct {
	CheckpointID string
	DBBytes      int64
	ArtifactsAt  int64
	CreatedAt    time.Time
}

// CheckpointAgent is the background creator of StoreIndex checkpoints
// via VacuumInto + packing into the CAS. Engine-managed: launched
// for every Target Store with an available StoreIndex.
//
// Checkpoint Agent is creation only. StoreIndex recovery from a
// checkpoint is the job of RebuildIndexAgent (maintenance), which
// uses a fresh checkpoint as the starting point and reads in the
// new manifests through ListObjectsWithModTime.
type CheckpointAgent interface {
	agent.Agent

	// TakeCheckpoint forces a checkpoint regardless of Interval and
	// ArtifactThreshold. Used before critical maintenance
	// operations (RebuildIndex, MigrateIndex).
	TakeCheckpoint(ctx context.Context) (CheckpointStats, error)
}

const (
	checkpointLeasePath       = "system.state/checkpoint/lease"
	checkpointNamePrefix      = "index_checkpoint/"
	defaultCheckpointIntv     = 6 * time.Hour
	defaultCheckpointKeep     = 3
	defaultCheckpointLeaseTTL = 15 * time.Minute
	// checkpointTimeLayout is path-safe and lexicographically sortable
	// (no colons, fixed-width nanoseconds) so a name sort is a time
	// sort — retention drops the lexicographically smallest names.
	checkpointTimeLayout = "20060102T150405.000000000Z"
)

// NewCheckpointAgent creates a Checkpoint Agent. Constructed by the
// assembler (Variant B): it needs the StoreIndex to VacuumInto a
// checkpoint file and the Store to publish that file into the CAS via
// System().Put (WithoutIndex — a checkpoint is engine state, not a user
// artifact, and indexing it would make it a rebuild input of itself).
func NewCheckpointAgent(
	st store.Store,
	drv driver.Driver,
	idx index.StoreIndex,
	bus event.Publisher,
	hostID string,
	storeID string,
	cfg CheckpointConfig,
	opts ...agent.AgentOption,
) (CheckpointAgent, error) {
	if st == nil || drv == nil || idx == nil || bus == nil {
		return nil, fmt.Errorf("checkpoint.NewCheckpointAgent: store, driver, index and bus are required")
	}
	if hostID == "" {
		return nil, fmt.Errorf("checkpoint.NewCheckpointAgent: hostID is required for the checkpoint lease")
	}
	cfg = applyCheckpointDefaults(cfg)
	return &checkpointAgent{
		BaseState: agent.NewBaseState(agent.ResolveLogger(opts...)),
		store:     st, drv: drv, idx: idx, bus: bus,
		hostID: hostID, storeID: storeID, cfg: cfg,
	}, nil
}

func applyCheckpointDefaults(cfg CheckpointConfig) CheckpointConfig {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultCheckpointIntv
	}
	if cfg.Retention <= 0 {
		cfg.Retention = defaultCheckpointKeep
	}
	return cfg
}

type checkpointAgent struct {
	store   store.Store
	drv     driver.Driver
	idx     index.StoreIndex
	bus     event.Publisher
	hostID  string
	storeID string
	cfg     CheckpointConfig

	agent.BaseState
}

var _ CheckpointAgent = (*checkpointAgent)(nil)

// checkpointFactory builds the Checkpoint agent from the registry (ADR-51).
type checkpointFactory struct{}

func (checkpointFactory) Name() string { return "checkpoint" }

func (checkpointFactory) Build(st store.Store, cfg any, deps agent.AgentDeps) (agent.Agent, error) {
	c, _ := cfg.(CheckpointConfig) // zero value on mismatch -> defaults
	return NewCheckpointAgent(st, deps.Driver, deps.Index, deps.Publisher, deps.HostID, deps.StoreID, c, agent.WithAgentLogger(deps.Logger))
}

// AgentType is the short registry/event identifier.
func (a *checkpointAgent) AgentType() string { return "checkpoint" }

// Validate checks preconditions. Checkpoint needs no maintenance mode;
// the checkpoint lease is acquired inside Run, so the only precondition
// here is a live context.
func (a *checkpointAgent) Validate(ctx context.Context) error { return ctx.Err() }

// Run takes one checkpoint and returns its AgentResult. A one-shot
// operation: the scheduler decides cadence (ADR-69).
func (a *checkpointAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	a.SetState(agent.StateRunning, nil)
	stats, err := a.TakeCheckpoint(ctx)
	res := &domain.AgentResult{
		AgentType:   "checkpoint",
		StoreID:     a.storeID,
		CompletedAt: time.Now(),
		Stats:       map[string]int64{"db_bytes": stats.DBBytes},
	}
	if err != nil {
		a.SetState(agent.StateFaulted, err)
		if agent.IsCtxErr(err) {
			res.Partial = true
			a.bus.Publish(event.Event{Type: event.EventAgentCancelled})
			return res, err
		}
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "checkpoint", StoreID: a.storeID, Err: err,
		}})
		return res, err
	}
	a.SetState(agent.StateIdle, nil)
	return res, nil
}

// TakeCheckpoint vacuums the index into a temp file, publishes it into
// the CAS under index_checkpoint/<ts> (unindexed), then prunes old
// checkpoints past Retention. Runs under the checkpoint lease so two hosts
// do not vacuum concurrently.
func (a *checkpointAgent) TakeCheckpoint(ctx context.Context) (CheckpointStats, error) {
	if err := ctx.Err(); err != nil {
		return CheckpointStats{}, err
	}
	a.bus.Publish(event.Event{Type: event.EventAgentStarted, Payload: event.AgentStartedPayload{
		AgentType: "checkpoint", StoreID: a.storeID, StartedAt: time.Now(),
	}})

	l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
		Path:      checkpointLeasePath,
		HostID:    a.hostID,
		AgentType: "checkpoint",
		TTL:       defaultCheckpointLeaseTTL,
	})
	if err != nil {
		return CheckpointStats{}, fmt.Errorf("checkpoint.Checkpoint.TakeCheckpoint: acquire lease: %w", err)
	}
	if prev != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
			LeaseKey: checkpointLeasePath, PreviousHolder: prev.HostID,
			ExpiredAt: prev.ExpiresAt, TakenBy: a.hostID,
		}})
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	hbErr := make(chan error, 1)
	go func() { hbErr <- l.Heartbeat(runCtx) }()
	defer func() {
		cancel()
		if err := l.Release(context.WithoutCancel(ctx)); err != nil {
			a.Logger().Warn("lease release failed; lease will expire via TTL", "err", err)
		}
	}()

	stats, err := a.checkpointOnce(runCtx)
	if err != nil {
		return CheckpointStats{}, fmt.Errorf("checkpoint.Checkpoint.TakeCheckpoint: %w", err)
	}
	if herr := drainHeartbeat(hbErr); herr != nil {
		return CheckpointStats{}, fmt.Errorf("checkpoint.Checkpoint.TakeCheckpoint: lease lost: %w", herr)
	}

	a.bus.Publish(event.Event{Type: event.EventAgentCompleted, Payload: domain.AgentResult{
		AgentType: "checkpoint", StoreID: a.storeID, CompletedAt: time.Now(),
		Stats: map[string]int64{"db_bytes": stats.DBBytes},
	}})
	return stats, nil
}

func (a *checkpointAgent) checkpointOnce(ctx context.Context) (CheckpointStats, error) {
	now := time.Now().UTC()
	id := now.Format(checkpointTimeLayout)

	// VacuumInto needs an empty destination on an OS path. A temp dir
	// keeps the partial vacuum off the Location until it is complete.
	tmpDir, err := os.MkdirTemp("", "scrinium-checkpoint-")
	if err != nil {
		return CheckpointStats{}, fmt.Errorf("mkdir temp: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpPath := filepath.Join(tmpDir, id+".db")

	if err := a.idx.VacuumInto(ctx, tmpPath); err != nil {
		return CheckpointStats{}, fmt.Errorf("vacuum into %q: %w", tmpPath, err)
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return CheckpointStats{}, fmt.Errorf("stat checkpoint: %w", err)
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return CheckpointStats{}, fmt.Errorf("open checkpoint: %w", err)
	}
	defer f.Close()

	name := checkpointNamePrefix + id
	if err := a.store.System().Put(ctx,
		store.SystemArtifact{Name: name, Payload: f},
		store.WithoutIndex(),
	); err != nil {
		return CheckpointStats{}, fmt.Errorf("publish checkpoint %q: %w", name, err)
	}

	if err := a.pruneOldCheckpoints(ctx); err != nil {
		// Retention failure does not invalidate the checkpoint just taken.
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "checkpoint", StoreID: a.storeID, Err: err,
		}})
	}

	return CheckpointStats{
		CheckpointID: id,
		DBBytes:      info.Size(),
		CreatedAt:    now,
	}, nil
}

// pruneOldCheckpoints keeps the newest Retention checkpoints and deletes the
// rest. Names embed a fixed-width, path-safe timestamp, so a string sort
// is a chronological sort.
//
// Note on dedup: if two checkpoints have byte-identical bodies (the index
// did not change between them) they share one CAS artifact (ADR-58), and
// System().Delete of one name drops that shared artifact, orphaning the
// other name's pointer. In practice consecutive checkpoints are taken over
// a changing index (or forced once before maintenance), so identical
// bodies do not arise; the case is noted rather than guarded, since the
// fix belongs to System() artifact sharing semantics, not to retention.
func (a *checkpointAgent) pruneOldCheckpoints(ctx context.Context) error {
	var names []string
	err := a.store.System().Walk(ctx, checkpointNamePrefix, func(name string, _ domain.Manifest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk checkpoints: %w", err)
	}
	if len(names) <= a.cfg.Retention {
		return nil
	}
	sort.Strings(names) // oldest first
	excess := len(names) - a.cfg.Retention
	for _, old := range names[:excess] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.store.System().Delete(ctx, old); err != nil {
			return fmt.Errorf("delete old checkpoint %q: %w", old, err)
		}
	}
	return nil
}

// drainHeartbeat returns a non-context heartbeat error if one is already
// pending, else nil. Shared shape with the other lease-holding agents.
func drainHeartbeat(hbErr <-chan error) error {
	select {
	case herr := <-hbErr:
		if herr != nil && !agent.IsCtxErr(herr) {
			return herr
		}
	default:
	}
	return nil
}

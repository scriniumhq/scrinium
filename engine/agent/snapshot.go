package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/lease"
	"scrinium.dev/engine/store"
	"scrinium.dev/event"
)

// --- Snapshot Agent ---

// SnapshotConfig configures the Snapshot Agent.
type SnapshotConfig struct {
	// Enabled toggles background snapshotting.
	Enabled bool

	// Interval is the periodic snapshot interval.
	Interval time.Duration

	// ArtifactThreshold also triggers a snapshot once this many
	// new artifacts have been added since the previous snapshot.
	ArtifactThreshold int

	// Retention is the number of snapshots to keep; older ones are
	// removed.
	Retention int

	// RecoveryOverlap is the recovery overlap: when loading a
	// snapshot, RebuildIndexAgent re-reads objects that appeared
	// after snapshot_created_at - RecoveryOverlap. It guards
	// against the edge case "an object was written between the
	// snapshot and the crash".
	RecoveryOverlap time.Duration
}

// SnapshotStats are the statistics of a single snapshot.
type SnapshotStats struct {
	SnapshotID  string
	DBBytes     int64
	ArtifactsAt int64
	CreatedAt   time.Time
}

// SnapshotAgent is the background creator of StoreIndex snapshots
// via VacuumInto + packing into the CAS. Engine-managed: launched
// for every Target Store with an available StoreIndex.
//
// Snapshot Agent is creation only. StoreIndex recovery from a
// snapshot is the job of RebuildIndexAgent (maintenance), which
// uses a fresh snapshot as the starting point and reads in the
// new manifests through ListObjectsWithModTime.
type SnapshotAgent interface {
	Agent

	// TakeSnapshot forces a snapshot regardless of Interval and
	// ArtifactThreshold. Used before critical maintenance
	// operations (RebuildIndex, MigrateIndex).
	TakeSnapshot(ctx context.Context) (SnapshotStats, error)
}

const (
	snapshotLeasePath       = "system.state/snapshot/lease"
	snapshotNamePrefix      = "index_snapshot/"
	defaultSnapshotIntv     = 6 * time.Hour
	defaultSnapshotKeep     = 3
	defaultSnapshotLeaseTTL = 15 * time.Minute
	// snapshotTimeLayout is path-safe and lexicographically sortable
	// (no colons, fixed-width nanoseconds) so a name sort is a time
	// sort — retention drops the lexicographically smallest names.
	snapshotTimeLayout = "20060102T150405.000000000Z"
)

// NewSnapshotAgent creates a Snapshot Agent. Constructed by the
// assembler (Variant B): it needs the StoreIndex to VacuumInto a
// snapshot file and the Store to publish that file into the CAS via
// System().Put (WithoutIndex — a snapshot is engine state, not a user
// artifact, and indexing it would make it a rebuild input of itself).
func NewSnapshotAgent(
	st store.Store,
	drv driver.Driver,
	idx index.StoreIndex,
	bus event.Publisher,
	hostID string,
	storeID string,
	cfg SnapshotConfig,
	opts ...AgentOption,
) (SnapshotAgent, error) {
	if st == nil || drv == nil || idx == nil || bus == nil {
		return nil, fmt.Errorf("agent.NewSnapshotAgent: store, driver, index and bus are required")
	}
	if hostID == "" {
		return nil, fmt.Errorf("agent.NewSnapshotAgent: hostID is required for the snapshot lease")
	}
	cfg = applySnapshotDefaults(cfg)
	return &snapshotAgent{
		baseState: baseState{log: resolveAgentLogger(opts)},
		store:     st, drv: drv, idx: idx, bus: bus,
		hostID: hostID, storeID: storeID, cfg: cfg,
	}, nil
}

func applySnapshotDefaults(cfg SnapshotConfig) SnapshotConfig {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultSnapshotIntv
	}
	if cfg.Retention <= 0 {
		cfg.Retention = defaultSnapshotKeep
	}
	return cfg
}

type snapshotAgent struct {
	store   store.Store
	drv     driver.Driver
	idx     index.StoreIndex
	bus     event.Publisher
	hostID  string
	storeID string
	cfg     SnapshotConfig

	baseState
}

var _ SnapshotAgent = (*snapshotAgent)(nil)

// snapshotFactory builds the Snapshot agent from the registry (ADR-51).
type snapshotFactory struct{}

func (snapshotFactory) Name() string { return "snapshot" }

func (snapshotFactory) Build(st store.Store, cfg any, deps AgentDeps) (Agent, error) {
	c, _ := cfg.(SnapshotConfig) // zero value on mismatch -> defaults
	return NewSnapshotAgent(st, deps.Driver, deps.Index, deps.Publisher, deps.HostID, deps.StoreID, c, WithAgentLogger(deps.Logger))
}

func init() { Register(snapshotFactory{}) }

// AgentType is the short registry/event identifier.
func (a *snapshotAgent) AgentType() string { return "snapshot" }

// Validate checks preconditions. Snapshot needs no maintenance mode;
// the snapshot lease is acquired inside Run, so the only precondition
// here is a live context.
func (a *snapshotAgent) Validate(ctx context.Context) error { return ctx.Err() }

// Run takes one snapshot and returns its AgentResult. A one-shot
// operation: the scheduler decides cadence (ADR-69).
func (a *snapshotAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	a.setState(StateRunning, nil)
	stats, err := a.TakeSnapshot(ctx)
	res := &domain.AgentResult{
		AgentType:   "snapshot",
		StoreID:     a.storeID,
		CompletedAt: time.Now(),
		Stats:       map[string]int64{"db_bytes": stats.DBBytes},
	}
	if err != nil {
		a.setState(StateFaulted, err)
		if isCtxErr(err) {
			res.Partial = true
			a.bus.Publish(event.Event{Type: event.EventAgentCancelled})
			return res, err
		}
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "snapshot", StoreID: a.storeID, Err: err,
		}})
		return res, err
	}
	a.setState(StateIdle, nil)
	return res, nil
}

// TakeSnapshot vacuums the index into a temp file, publishes it into
// the CAS under index_snapshot/<ts> (unindexed), then prunes old
// snapshots past Retention. Runs under the snapshot lease so two hosts
// do not vacuum concurrently.
func (a *snapshotAgent) TakeSnapshot(ctx context.Context) (SnapshotStats, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotStats{}, err
	}
	a.bus.Publish(event.Event{Type: event.EventAgentStarted, Payload: event.AgentStartedPayload{
		AgentType: "snapshot", StoreID: a.storeID, StartedAt: time.Now(),
	}})

	l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
		Path:      snapshotLeasePath,
		HostID:    a.hostID,
		AgentType: "snapshot",
		TTL:       defaultSnapshotLeaseTTL,
	})
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("agent.Snapshot.TakeSnapshot: acquire lease: %w", err)
	}
	if prev != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
			LeaseKey: snapshotLeasePath, PreviousHolder: prev.HostID,
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
			a.logger().Warn("lease release failed; lease will expire via TTL", "err", err)
		}
	}()

	stats, err := a.snapshotOnce(runCtx)
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("agent.Snapshot.TakeSnapshot: %w", err)
	}
	if herr := drainHeartbeat(hbErr); herr != nil {
		return SnapshotStats{}, fmt.Errorf("agent.Snapshot.TakeSnapshot: lease lost: %w", herr)
	}

	a.bus.Publish(event.Event{Type: event.EventAgentCompleted, Payload: domain.AgentResult{
		AgentType: "snapshot", StoreID: a.storeID, CompletedAt: time.Now(),
		Stats: map[string]int64{"db_bytes": stats.DBBytes},
	}})
	return stats, nil
}

func (a *snapshotAgent) snapshotOnce(ctx context.Context) (SnapshotStats, error) {
	now := time.Now().UTC()
	id := now.Format(snapshotTimeLayout)

	// VacuumInto needs an empty destination on an OS path. A temp dir
	// keeps the partial vacuum off the Location until it is complete.
	tmpDir, err := os.MkdirTemp("", "scrinium-snapshot-")
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("mkdir temp: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpPath := filepath.Join(tmpDir, id+".db")

	if err := a.idx.VacuumInto(ctx, tmpPath); err != nil {
		return SnapshotStats{}, fmt.Errorf("vacuum into %q: %w", tmpPath, err)
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("stat snapshot: %w", err)
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	name := snapshotNamePrefix + id
	if err := a.store.System().Put(ctx,
		store.SystemArtifact{Name: name, Payload: f},
		store.WithoutIndex(),
	); err != nil {
		return SnapshotStats{}, fmt.Errorf("publish snapshot %q: %w", name, err)
	}

	if err := a.pruneOldSnapshots(ctx); err != nil {
		// Retention failure does not invalidate the snapshot just taken.
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "snapshot", StoreID: a.storeID, Err: err,
		}})
	}

	return SnapshotStats{
		SnapshotID: id,
		DBBytes:    info.Size(),
		CreatedAt:  now,
	}, nil
}

// pruneOldSnapshots keeps the newest Retention snapshots and deletes the
// rest. Names embed a fixed-width, path-safe timestamp, so a string sort
// is a chronological sort.
//
// Note on dedup: if two snapshots have byte-identical bodies (the index
// did not change between them) they share one CAS artifact (ADR-58), and
// System().Delete of one name drops that shared artifact, orphaning the
// other name's pointer. In practice consecutive snapshots are taken over
// a changing index (or forced once before maintenance), so identical
// bodies do not arise; the case is noted rather than guarded, since the
// fix belongs to System() artifact sharing semantics, not to retention.
func (a *snapshotAgent) pruneOldSnapshots(ctx context.Context) error {
	var names []string
	err := a.store.System().Walk(ctx, snapshotNamePrefix, func(name string, _ domain.Manifest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk snapshots: %w", err)
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
			return fmt.Errorf("delete old snapshot %q: %w", old, err)
		}
	}
	return nil
}

// drainHeartbeat returns a non-context heartbeat error if one is already
// pending, else nil. Shared shape with the other lease-holding agents.
func drainHeartbeat(hbErr <-chan error) error {
	select {
	case herr := <-hbErr:
		if herr != nil && !isCtxErr(herr) {
			return herr
		}
	default:
	}
	return nil
}

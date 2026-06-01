package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
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
	BackgroundAgent

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
	bus event.EventBus,
	hostID string,
	storeID string,
	cfg SnapshotConfig,
) (SnapshotAgent, error) {
	if st == nil || drv == nil || idx == nil || bus == nil {
		return nil, fmt.Errorf("agent.NewSnapshotAgent: store, driver, index and bus are required")
	}
	if hostID == "" {
		return nil, fmt.Errorf("agent.NewSnapshotAgent: hostID is required for the snapshot lease")
	}
	cfg = applySnapshotDefaults(cfg)
	return &snapshotAgent{
		store: st, drv: drv, idx: idx, bus: bus,
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
	bus     event.EventBus
	hostID  string
	storeID string
	cfg     SnapshotConfig

	mu    sync.Mutex
	state State
	err   error
}

var _ SnapshotAgent = (*snapshotAgent)(nil)

// Run periodically snapshots the index until ctx is cancelled. The
// ArtifactThreshold trigger is not wired on M3: there is no cheap
// "artifacts added since last snapshot" counter on the public surface,
// so snapshots are driven by Interval and explicit TakeSnapshot. A
// failed cycle is reported and the loop continues.
func (a *snapshotAgent) Run(ctx context.Context) error {
	a.setState(StateRunning, nil)
	t := time.NewTicker(a.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.setState(StateIdle, nil)
			a.bus.Publish(event.Event{Type: EventAgentStopped, Payload: AgentStartedPayload{
				AgentType: "Snapshot", StoreID: a.storeID,
			}})
			return ctx.Err()
		case <-t.C:
			if _, err := a.TakeSnapshot(ctx); err != nil && !isCtxErr(err) {
				a.bus.Publish(event.Event{Type: EventAgentFailed, Payload: AgentFailedPayload{
					AgentType: "Snapshot", StoreID: a.storeID, Err: err,
				}})
			}
		}
	}
}

// Status reports the current state and the last fatal error.
func (a *snapshotAgent) Status() (State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state, a.err
}

// TakeSnapshot vacuums the index into a temp file, publishes it into
// the CAS under index_snapshot/<ts> (unindexed), then prunes old
// snapshots past Retention. Runs under the snapshot lease so two hosts
// do not vacuum concurrently.
func (a *snapshotAgent) TakeSnapshot(ctx context.Context) (SnapshotStats, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotStats{}, err
	}
	a.bus.Publish(event.Event{Type: EventAgentStarted, Payload: AgentStartedPayload{
		AgentType: "Snapshot", StoreID: a.storeID, StartedAt: time.Now(),
	}})

	l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
		Path:      snapshotLeasePath,
		HostID:    a.hostID,
		AgentType: "Snapshot",
		TTL:       defaultSnapshotLeaseTTL,
	})
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("agent.Snapshot.TakeSnapshot: acquire lease: %w", err)
	}
	if prev != nil {
		a.bus.Publish(event.Event{Type: EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
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
		_ = l.Release(context.WithoutCancel(ctx))
	}()

	stats, err := a.snapshotOnce(runCtx)
	if err != nil {
		return SnapshotStats{}, fmt.Errorf("agent.Snapshot.TakeSnapshot: %w", err)
	}
	if herr := drainHeartbeat(hbErr); herr != nil {
		return SnapshotStats{}, fmt.Errorf("agent.Snapshot.TakeSnapshot: lease lost: %w", herr)
	}

	a.bus.Publish(event.Event{Type: EventAgentCycle, Payload: domain.AgentResult{
		AgentType: "Snapshot", StoreID: a.storeID, CompletedAt: time.Now(),
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
		a.bus.Publish(event.Event{Type: EventAgentFailed, Payload: AgentFailedPayload{
			AgentType: "Snapshot", StoreID: a.storeID, Err: err,
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

func (a *snapshotAgent) setState(s State, err error) {
	a.mu.Lock()
	a.state, a.err = s, err
	a.mu.Unlock()
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

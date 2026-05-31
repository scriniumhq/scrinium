package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/lease"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// GCConfig configures the GC Agent.
type GCConfig struct {
	// ScanInterval is the interval between Mark+Sweep cycles.
	ScanInterval time.Duration

	// BatchSize is the number of blobs in a single StoreIndex
	// transaction during a scan.
	BatchSize int

	// LeaseTTL is the hold time for gc/lease under
	// GCLeasePolicy: LeaderElection. Renew runs at half the TTL.
	LeaseTTL time.Duration

	// CompactionEnabled toggles compaction of partially dead .pack
	// volumes. The default in v1 is false (a deferred feature; see
	// docs/2. Internals/05 Asynchronous Engine §5.3.7).
	CompactionEnabled bool

	// CompactionThreshold — a dead_ratio at or above this value
	// triggers compaction. Default 0.5.
	CompactionThreshold float64

	// MinPackAge — packs younger than this are not compacted.
	// Fresh packs may collapse on their own without paying a
	// repacking cost.
	MinPackAge time.Duration

	// MaxCompactionsPerCycle bounds the I/O load per GC cycle.
	MaxCompactionsPerCycle int
}

// GCStats are the statistics of a single GC cycle.
type GCStats struct {
	ScannedBlobs        int64
	MarkedBlobs         int64
	RemovedBlobs        int64
	FreedBytes          int64
	CompactedPacks      int64
	CompactedFreedBytes int64
}

// GCAgent is the background reaper of orphan blobs governed by
// Two-Phase Deletion. User-managed: the engine does not start the GC
// automatically — the deletion policy is a deployment-specific
// decision.
type GCAgent interface {
	BackgroundAgent

	// RunOnce executes one full Mark+Sweep cycle and returns. Used
	// for manual runs and tests; unlike Run it does not block on
	// ScanInterval.
	RunOnce(ctx context.Context) (GCStats, error)
}

// NewGCAgent creates a GC Agent. Constructed by the assembler with the
// raw Driver and StoreIndex alongside store.Store: Mark/Sweep walk
// ListOrphanBlobs on the index and act on tombstone files through the
// driver, while StoreConfig (TombstoneGracePeriod, GCLeasePolicy) is
// read from the Store's Config(). hostID owns the gc lease under
// LeaderElection; storeID tags events.
//
// Per-store by invariant (§5.3.2): the agent only ever touches its own
// Location. Cross-store moves are between two indexes, so GC never
// competes with migration over a blob in one index.
func NewGCAgent(
	st store.Store,
	drv driver.Driver,
	idx index.StoreIndex,
	bus event.EventBus,
	hostID string,
	storeID string,
	cfg GCConfig,
) (GCAgent, error) {
	if st == nil || drv == nil || idx == nil || bus == nil {
		return nil, fmt.Errorf("agent.NewGCAgent: store, driver, index and bus are required")
	}
	if hostID == "" {
		return nil, fmt.Errorf("agent.NewGCAgent: hostID is required for the gc lease")
	}
	cfg = applyGCDefaults(cfg)
	return &gcAgent{
		store: st, drv: drv, idx: idx, bus: bus,
		hostID: hostID, storeID: storeID, cfg: cfg,
	}, nil
}

const (
	gcLeasePath           = "system.state/gc/lease"
	defaultGCScanInterval = 1 * time.Hour
	defaultGCBatchSize    = 100
	defaultGCLeaseTTL     = 5 * time.Minute
	defaultMinPackAge     = 168 * time.Hour // 7 days
)

func applyGCDefaults(cfg GCConfig) GCConfig {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultGCScanInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultGCBatchSize
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultGCLeaseTTL
	}
	if cfg.CompactionThreshold <= 0 {
		cfg.CompactionThreshold = 0.5
	}
	if cfg.MinPackAge <= 0 {
		cfg.MinPackAge = defaultMinPackAge
	}
	return cfg
}

// gcAgent is the concrete GCAgent.
type gcAgent struct {
	store   store.Store
	drv     driver.Driver
	idx     index.StoreIndex
	bus     event.EventBus
	hostID  string
	storeID string
	cfg     GCConfig

	mu    sync.Mutex
	state State
	err   error
}

var _ GCAgent = (*gcAgent)(nil)

// Run is the background loop: one Mark+Sweep cycle every ScanInterval
// until ctx is cancelled. A failed cycle (lease lost, fatal index
// error) is reported via the failed event and the loop continues.
func (a *gcAgent) Run(ctx context.Context) error {
	a.setState(StateRunning, nil)
	t := time.NewTicker(a.cfg.ScanInterval)
	defer t.Stop()

	if _, err := a.RunOnce(ctx); err != nil && !isCtxErr(err) {
		a.bus.Publish(event.Event{Type: EventAgentFailed, Payload: AgentFailedPayload{
			AgentType: "GC", StoreID: a.storeID, Err: err,
		}})
	}
	for {
		select {
		case <-ctx.Done():
			a.setState(StateIdle, nil)
			a.bus.Publish(event.Event{Type: EventAgentStopped, Payload: AgentStartedPayload{
				AgentType: "GC", StoreID: a.storeID,
			}})
			return ctx.Err()
		case <-t.C:
			if _, err := a.RunOnce(ctx); err != nil && !isCtxErr(err) {
				a.bus.Publish(event.Event{Type: EventAgentFailed, Payload: AgentFailedPayload{
					AgentType: "GC", StoreID: a.storeID, Err: err,
				}})
			}
		}
	}
}

// Status reports the current state and the last fatal error.
func (a *gcAgent) Status() (State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state, a.err
}

// RunOnce executes one Mark+Sweep cycle. Under GCLeasePolicy:
// LeaderElection it first acquires the gc lease (SingleHost relies on
// location.lock and takes none). Mark tombstones every ref_count=0 blob;
// Sweep removes tombstone files whose grace period has elapsed and drops
// their index rows.
func (a *gcAgent) RunOnce(ctx context.Context) (GCStats, error) {
	if err := ctx.Err(); err != nil {
		return GCStats{}, err
	}
	a.bus.Publish(event.Event{Type: EventAgentStarted, Payload: AgentStartedPayload{
		AgentType: "GC", StoreID: a.storeID, StartedAt: time.Now(),
	}})

	cfg := a.store.Config()

	// Conditional lease: only LeaderElection coordinates via gc/lease.
	var hbErr <-chan error
	runCtx := ctx
	if a.leaseRequired(cfg.GCLeasePolicy) {
		l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
			Path:      gcLeasePath,
			HostID:    a.hostID,
			AgentType: "GC",
			TTL:       a.cfg.LeaseTTL,
		})
		if err != nil {
			return GCStats{}, fmt.Errorf("agent.GC.RunOnce: acquire lease: %w", err)
		}
		if prev != nil {
			a.bus.Publish(event.Event{Type: EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
				LeaseKey: gcLeasePath, PreviousHolder: prev.HostID,
				ExpiredAt: prev.ExpiresAt, TakenBy: a.hostID,
			}})
		}
		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(ctx)
		ch := make(chan error, 1)
		go func() { ch <- l.Heartbeat(runCtx) }()
		hbErr = ch
		defer func() {
			cancel()
			_ = l.Release(context.WithoutCancel(ctx))
		}()
	}

	var stats GCStats
	grace := cfg.TombstoneGracePeriod

	markErr := a.mark(runCtx, &stats)
	sweepErr := a.sweep(runCtx, grace, &stats)

	a.bus.Publish(event.Event{Type: EventAgentCycle, Payload: domain.AgentResult{
		AgentType: "GC", StoreID: a.storeID, CompletedAt: time.Now(),
		Stats: map[string]int64{
			"scanned_blobs": stats.ScannedBlobs,
			"marked_blobs":  stats.MarkedBlobs,
			"removed_blobs": stats.RemovedBlobs,
			"freed_bytes":   stats.FreedBytes,
		},
	}})

	if err := firstNonCtxErr(markErr, sweepErr); err != nil {
		return stats, fmt.Errorf("agent.GC.RunOnce: %w", err)
	}
	if hbErr != nil {
		select {
		case herr := <-hbErr:
			if herr != nil && !isCtxErr(herr) {
				return stats, fmt.Errorf("agent.GC.RunOnce: lease lost: %w", herr)
			}
		default:
		}
	}
	return stats, nil
}

// leaseRequired decides whether this cycle coordinates via gc/lease.
// LeaderElection always does; SingleHost never does (location.lock
// already guarantees a single process). Auto defers to SingleHost here
// — multi-host promotion is a deployment decision the assembler makes
// explicit, not something GC infers at runtime.
func (a *gcAgent) leaseRequired(policy domain.GCLeasePolicy) bool {
	return policy == domain.GCLeaseLeaderElection
}

// mark tombstones every orphan blob. Orphan refs are collected first,
// then acted on — MarkTombstone/Resolve must not run while the
// ListOrphanBlobs cursor is open (nested index/driver work under an open
// cursor deadlocks the connection pool).
func (a *gcAgent) mark(ctx context.Context, stats *GCStats) error {
	var refs []string
	listErr := a.idx.ListOrphanBlobs(ctx, func(blobRef string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		refs = append(refs, blobRef)
		return nil
	})
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats.ScannedBlobs++
		addr, err := a.idx.Resolve(ctx, ref)
		if err != nil {
			if errors.Is(err, errs.ErrArtifactNotFound) {
				continue // raced with a parallel sweep
			}
			return err
		}
		if addr.Path == "" {
			// Packed blob (lives inside a .pack); standalone-file GC
			// does not tombstone it. Pack compaction (deferred) owns
			// those. Skip.
			continue
		}
		if err := a.drv.MarkTombstone(ctx, addr.Path); err != nil {
			if isCtxErr(err) {
				return err
			}
			continue // transient driver error: next cycle retries
		}
		stats.MarkedBlobs++
	}
	return listErr
}

// sweep removes tombstone files past the grace period and drops their
// index rows. Same collect-then-act discipline as mark.
func (a *gcAgent) sweep(ctx context.Context, grace time.Duration, stats *GCStats) error {
	var refs []string
	listErr := a.idx.ListOrphanBlobs(ctx, func(blobRef string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		refs = append(refs, blobRef)
		return nil
	})
	now := time.Now()
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		addr, err := a.idx.Resolve(ctx, ref)
		if err != nil {
			if errors.Is(err, errs.ErrArtifactNotFound) {
				continue
			}
			return err
		}
		if addr.Path == "" {
			continue // packed; not a standalone tombstone
		}
		marked, since, err := a.drv.TombstoneInfo(ctx, addr.Path)
		if err != nil {
			if isCtxErr(err) {
				return err
			}
			continue
		}
		if !marked {
			continue // not tombstoned yet (a future Mark will catch it)
		}
		// Grace check: a marker is eligible once its age is at least
		// the grace period. grace == 0 means "sweep immediately", so
		// any marker qualifies — using >= (not >) and guarding the
		// zero case keeps sub-second ModTime jitter from skipping it.
		if grace > 0 && now.Sub(since) < grace {
			continue // still within grace
		}

		// Grace elapsed: physically remove the tombstone marker (NOT
		// addr.Path — Mark renamed the original to "<path>.tombstone",
		// so the original no longer exists). A missing marker means the
		// blob was Revived; RemoveTombstone treats it as a no-op.
		if err := a.drv.RemoveTombstone(ctx, addr.Path); err != nil {
			if isCtxErr(err) {
				return err
			}
			continue
		}
		// Drop the index row, but only if still an orphan: a Revive
		// between TombstoneInfo and here bumps ref_count, and
		// DeleteOrphanBlob then keeps the row.
		removed, err := a.idx.DeleteOrphanBlob(ctx, ref)
		if err != nil {
			if isCtxErr(err) {
				return err
			}
			continue
		}
		if !removed {
			continue // revived; leave it
		}
		stats.RemovedBlobs++
		stats.FreedBytes += addr.Size
		a.bus.Publish(event.Event{
			Type:    event.EventBlobPhysicallyDeleted,
			Payload: event.BlobPhysicallyDeletedPayload{BlobRef: ref},
		})
	}
	return listErr
}

func (a *gcAgent) setState(s State, err error) {
	a.mu.Lock()
	a.state, a.err = s, err
	a.mu.Unlock()
}

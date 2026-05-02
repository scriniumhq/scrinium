package agent

import (
	"context"
	"errors"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/event"
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
// Two-Phase Deletion. User-managed: Curator does not start the GC
// automatically — the deletion policy is a deployment-specific
// decision.
type GCAgent interface {
	BackgroundAgent

	// RunOnce executes one full Mark+Sweep cycle and returns. Used
	// for manual runs and tests; unlike Run it does not block on
	// ScanInterval.
	RunOnce(ctx context.Context) (GCStats, error)
}

// NewGCAgent creates a GC Agent. Takes core.Store (not DataStore):
// GC needs both halves of the contract — AdminStore for reading
// StoreConfig (GCLeasePolicy, TombstoneGracePeriod, DeletionPolicy)
// and DataStore for WalkSystem (lease coordination).
//
// TODO(M3.2): two-phase GC with tombstone reaping.
func NewGCAgent(
	store core.Store,
	bus event.EventBus,
	cfg GCConfig,
) (GCAgent, error) {
	return nil, errors.New("agent.NewGCAgent: not implemented")
}

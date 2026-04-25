package maintenance

import (
	"context"
	"errors"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/curator"
	"github.com/rkurbatov/scrinium/event"
)

// RebuildSource is the strategy for picking a source when
// rebuilding the index.
type RebuildSource string

const (
	// RebuildSourceAuto — use a snapshot if available; otherwise
	// fall back to a full scan.
	RebuildSourceAuto RebuildSource = "Auto"

	// RebuildSourceSnapshot — requires a valid snapshot; returns
	// ErrNoSnapshot when none is available.
	RebuildSourceSnapshot RebuildSource = "Snapshot"

	// RebuildSourceFullScan — ignores any snapshots; always does a
	// full Location scan.
	RebuildSourceFullScan RebuildSource = "FullScan"
)

// RebuildConfig configures the RebuildIndexAgent.
type RebuildConfig struct {
	// Source is the strategy: Auto (default), Snapshot, or
	// FullScan.
	Source RebuildSource

	// RecoveryKit holds the Recovery Kit content as bytes. Required
	// when the Store is in StateCorrupted because every descriptor
	// replica was lost (store.json missing or invalid). Otherwise
	// nil.
	RecoveryKit []byte

	// HostStorage is passed when StoreConfig.ManifestStorage is
	// Local or Replicated. When omitted in those modes:
	//   - Local → ErrHostStorageRequired during Validate;
	//   - Replicated → fallback to Remote.
	HostStorage curator.TransitStore

	// BatchSize is the number of manifests per IndexManifest
	// transaction. Default 1000. A larger value is faster but
	// holds the StoreIndex lease for longer.
	BatchSize int

	// Parallelism is the number of workers reading manifests in
	// parallel from the Location. Default 8. For S3 16–32 makes
	// sense, for LocalFS 4–8.
	Parallelism int

	// LeaseTTL is the hold time for system.state/maintenance/lease.
	// Default 30m. For very large Stores (millions of manifests)
	// it makes sense to extend it — losing the lease aborts the
	// operation.
	LeaseTTL time.Duration
}

// RebuildStats are the final statistics of the operation and a
// progress snapshot.
type RebuildStats struct {
	// Source is the path actually taken (relevant for Auto).
	Source RebuildSource

	// SnapshotUsed is the snapshot ID when Source != FullScan; an
	// empty string when starting from scratch.
	SnapshotUsed string

	// ManifestsScanned — total manifests read from the Location.
	ManifestsScanned int64

	// ManifestsIndexed — added to the StoreIndex.
	ManifestsIndexed int64

	// ManifestsSkipped — already in the snapshot, not re-read.
	ManifestsSkipped int64

	// BlobsRegistered — rows in the blobs table (regular + chunks).
	BlobsRegistered int64

	// PacksIndexed — pack volume TOCs read and parsed.
	PacksIndexed int64

	// PointerRecovered — was system.config/current restored?
	PointerRecovered bool

	// DescriptorRewrote — was store.json rewritten from the
	// Recovery Kit?
	DescriptorRewrote bool

	// Duration is the operation's elapsed time.
	Duration time.Duration
}

// RebuildIndexAgent rebuilds the StoreIndex from manifests. It
// supports a fast path through a recent snapshot with read-in of
// new objects and a full fallback Location scan. It is also used to
// restore store.json (when lost) and the system.config/current
// pointer (when dangling).
type RebuildIndexAgent interface {
	core.MaintenanceAgent

	// Stats returns a progress snapshot during execution (safe to
	// call from another goroutine). After Run, returns the final
	// statistics.
	Stats() RebuildStats
}

// NewRebuildIndexAgent creates a RebuildIndexAgent instance. Takes
// core.Store (not DataStore): the agent reads StoreConfig through
// AdminStore.ConfigHistory(), drives the maintenance mode, and
// reaches the Driver and StoreIndex from inside the Store via
// core.Store.
//
// Implementation lands in M3.4.
func NewRebuildIndexAgent(
	store core.Store,
	bus event.EventBus,
	cfg RebuildConfig,
) (RebuildIndexAgent, error) {
	return nil, errors.New("maintenance.NewRebuildIndexAgent: not implemented")
}

// --- Sentinel errors of the maintenance agents ---

// ErrNoSnapshot — Validate with Source: Snapshot when no valid
// snapshots are available.
var ErrNoSnapshot = errors.New("maintenance: no valid snapshot for Source=Snapshot")

// ErrHostStorageRequired — Validate with ManifestStorage: Local
// without a HostStorage being passed in.
var ErrHostStorageRequired = errors.New("maintenance: HostStorage required for ManifestStorage=Local")

// ErrManifestsLost — at Run time with ManifestStorage: Local the
// local disk turned out to be physically empty. Blobs in the
// Location remain intact but become unresolvable orphans.
var ErrManifestsLost = errors.New("maintenance: manifests unavailable")

// ErrRecoveryKitRequired — Validate with the Store in Corrupted
// after every descriptor replica has been lost and RecoveryKit is
// nil in the configuration.
var ErrRecoveryKitRequired = errors.New("maintenance: RecoveryKit required (descriptor lost, encrypted store)")

// Compile-time sanity: context is imported and used.
var _ context.Context = context.Background()

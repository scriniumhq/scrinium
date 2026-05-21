package maintenance

import (
	"fmt"
	"time"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/wrapper/host"
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
	HostStorage host.TransitStore

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
	coreapi.MaintenanceAgent

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
// TODO(M3.4): rebuild StoreIndex from manifests / Recovery Kit.
func NewRebuildIndexAgent(
	store coreapi.Store,
	bus event.EventBus,
	cfg RebuildConfig,
) (RebuildIndexAgent, error) {
	return nil, fmt.Errorf("%w: maintenance.NewRebuildIndexAgent", errs.ErrNotImplemented)
}

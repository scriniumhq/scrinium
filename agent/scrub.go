package agent

import (
	"context"
	"errors"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/event"
)

// --- Scrub Agent ---

// ScrubConfig configures the Scrub Agent. The same type is also
// re-exported through curator.ScrubConfig for passing into
// curator.WithScrubConfig (Curator-managed launch).
type ScrubConfig struct {
	// Enabled toggles background verification.
	Enabled bool

	// ScanInterval is the interval between verification cycles.
	ScanInterval time.Duration

	// MaxAge — blobs whose last_verified_at is older than
	// now() - MaxAge are eligible for verification.
	MaxAge time.Duration

	// MaxAgeNativeChecksum is an extended MaxAge for blobs on
	// media that report CapNativeChecksum. Silent bit rot is
	// impossible there, so the verification rate can be lowered.
	MaxAgeNativeChecksum time.Duration

	// BatchSize is the number of blobs in a single StoreIndex
	// fetch.
	BatchSize int
}

// ScrubStats are the statistics of a single Scrub cycle.
type ScrubStats struct {
	ScannedBlobs  int64
	VerifiedBlobs int64
	FailedBlobs   int64
}

// ScrubAgent is the background blob-integrity verifier.
// Curator-managed: Curator automatically launches a single Scrub
// Agent for every registered Target Store.
type ScrubAgent interface {
	BackgroundAgent

	// RunOnce performs one full verification pass over every blob
	// whose last_verified_at is older than MaxAge and returns. Used
	// for ad hoc runs after media-corruption suspicions.
	RunOnce(ctx context.Context) (ScrubStats, error)
}

// NewScrubAgent creates a Scrub Agent instance. The constructor is
// public — the host application can create a ScrubAgent manually
// for a one-shot run or a custom integration. It is not required
// for normal operation under Curator: Curator creates instances on
// its own.
//
// Implementation lands in M3.3.
func NewScrubAgent(
	store core.Store,
	bus event.EventBus,
	cfg ScrubConfig,
) (ScrubAgent, error) {
	return nil, errors.New("agent.NewScrubAgent: not implemented")
}

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
// via VacuumInto + packing into the CAS. Curator-managed: launched
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

// NewSnapshotAgent creates a Snapshot Agent instance.
// Implementation lands in M3.3.
func NewSnapshotAgent(
	store core.Store,
	bus event.EventBus,
	cfg SnapshotConfig,
) (SnapshotAgent, error) {
	return nil, errors.New("agent.NewSnapshotAgent: not implemented")
}

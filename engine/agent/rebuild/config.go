package rebuild

import (
	"time"
)

// RebuildSource is the strategy for picking a source when
// rebuilding the index.
type RebuildSource string

const (
	// RebuildSourceAuto — use a checkpoint if available; otherwise
	// fall back to a full scan.
	RebuildSourceAuto RebuildSource = "Auto"

	// RebuildSourceCheckpoint — requires a valid checkpoint; returns
	// ErrNoCheckpoint when none is available.
	RebuildSourceCheckpoint RebuildSource = "Checkpoint"

	// RebuildSourceFullScan — ignores any checkpoints; always does a
	// full Location scan.
	RebuildSourceFullScan RebuildSource = "FullScan"
)

// RebuildConfig configures the RebuildIndexAgent.
type RebuildConfig struct {
	// Source is the strategy: Auto (default), Checkpoint, or
	// FullScan.
	Source RebuildSource

	// RecoveryKit holds the Recovery Kit content as bytes. Required
	// when the Store is in StateCorrupted because every descriptor
	// replica was lost (store.json missing or invalid). Otherwise
	// nil.
	RecoveryKit []byte

	// BatchSize is the number of manifests per IndexManifest
	// transaction. Default 1000. A larger value is faster but
	// holds the StoreIndex lease for longer.
	BatchSize int

	// Parallelism is the number of workers reading manifests in
	// parallel from the Location. Default 8. For S3 16–32 makes
	// sense, for LocalFS 4–8.
	Parallelism int

	// LeaseTTL is the hold time for store.state.maintenance.lease.
	// Default 30m. For very large Stores (millions of manifests)
	// it makes sense to extend it — losing the lease aborts the
	// operation.
	LeaseTTL time.Duration

	// RecoveryOverlap widens the tail re-scan when restoring from a
	// checkpoint: the scan re-reads manifests modified since
	// (checkpoint time − RecoveryOverlap) rather than exactly the
	// checkpoint instant. The overlap absorbs clock skew and manifests
	// written concurrently with the vacuum. IndexManifest is idempotent,
	// so re-reading a handful of already-present manifests is harmless.
	// Zero means no overlap (tail starts exactly at the checkpoint
	// instant); a small positive value (minutes) is recommended.
	RecoveryOverlap time.Duration

	// IgnoreStoreID, when true, skips the store-identity guard that rejects a
	// checkpoint recorded for a different Store before restoring it. Use only
	// to force recovery from a checkpoint whose identity is known-good despite
	// a mismatch (e.g. a deliberately imported checkpoint).
	IgnoreStoreID bool
}

const (
	rebuildLeasePath        = "store.state.maintenance.lease"
	defaultRebuildBatchSize = 1000
	defaultRebuildParallel  = 8
	defaultRebuildLeaseTTL  = 30 * time.Minute
	manifestsPrefix         = "manifests"
)

func applyRebuildDefaults(cfg RebuildConfig) RebuildConfig {
	if cfg.Source == "" {
		cfg.Source = RebuildSourceAuto
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultRebuildBatchSize
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = defaultRebuildParallel
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultRebuildLeaseTTL
	}
	return cfg
}

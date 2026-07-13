package store

import (
	"context"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
)

// Capacity returns aggregated storage info. Best-effort:
//
//   - ArtifactCount: user-visible manifests, from the index walked with
//     the "*" wildcard. System artifacts are excluded — Capacity
//     reports what users see through Walk, not the raw manifest count.
//   - BlobCount: physical count of files under "blobs/" via the Driver.
//     Inline manifests carry no separate blob file and do not appear.
//   - TotalBytes / AvailableBytes / UsedBytes come from the Driver's
//     optional CapacityReporter (statfs on a local volume): total and
//     available are the backing volume's, used is their difference. Drivers
//     that cannot report (object stores) leave them at the -1 "unavailable"
//     sentinel. This is volume capacity, not a byte-exact account of stored
//     data, which would need a full scan.
//
// Goes through enterRead, so Capacity refuses on closed, corrupted,
// offline, bootstrapping, or locked stores with the appropriate
// sentinel; operators can still read static metadata through State /
// Capabilities. Honours ctx cancellation between the two operations.
func (s *store) Capacity(ctx context.Context) (domain.StorageInfo, error) {
	if err := s.enterRead(ctx); err != nil {
		return domain.StorageInfo{}, err
	}

	out := domain.StorageInfo{
		TotalBytes:     -1,
		UsedBytes:      -1,
		AvailableBytes: -1,
	}

	// ArtifactCount: count of user-visible manifests. Iterates every
	// user manifest (artifact_id present), which already excludes
	// system artifacts (name-addressed, not indexed), so config.StoreConfig and
	// the future store.agent writers do not inflate user-facing storage stats.
	// Prefer a database-side count (ManifestCounter): iterating every
	// manifest just to increment a counter deserialises each row for nothing.
	// Fall back to the walk for indexes without the capability.
	var artifactCount int64
	if counter, ok := s.index.(index.ManifestCounter); ok {
		n, err := counter.CountManifests(ctx)
		if err != nil {
			return domain.StorageInfo{}, fmt.Errorf("store.Capacity: count manifests: %w", err)
		}
		artifactCount = n
	} else {
		if err := s.index.IterateManifests(ctx, func(domain.Manifest) error {
			artifactCount++
			return nil
		}); err != nil {
			return domain.StorageInfo{}, fmt.Errorf("store.Capacity: count manifests: %w", err)
		}
	}
	out.ArtifactCount = artifactCount

	if err := ctx.Err(); err != nil {
		return domain.StorageInfo{}, err
	}

	// BlobCount: physical blobs/ count. Inline manifests carry no
	// separate blob file, so they do not contribute here.
	blobs, err := s.drv.CountObjects(ctx, "blobs")
	if err != nil {
		return domain.StorageInfo{}, fmt.Errorf("store.Capacity: count blobs: %w", err)
	}
	out.BlobCount = blobs

	// Disk capacity: best-effort through the Driver's optional
	// CapacityReporter. Local-filesystem drivers answer via statfs; object
	// stores do not implement it, leaving the -1 sentinels untouched.
	if cr, ok := s.drv.(driver.CapacityReporter); ok {
		if total, avail, derr := cr.DiskUsage(ctx); derr == nil && total >= 0 && avail >= 0 {
			out.TotalBytes = total
			out.AvailableBytes = avail
			out.UsedBytes = total - avail
		}
	}

	return out, nil
}

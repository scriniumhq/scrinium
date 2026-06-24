package store

import (
	"context"
	"fmt"

	"scrinium.dev/domain"
)

// Capacity returns aggregated storage info. Best-effort:
//
//   - ArtifactCount: user-visible manifests, from the index walked with
//     the "*" wildcard. System artifacts are excluded — Capacity
//     reports what users see through Walk, not the raw manifest count.
//   - BlobCount: physical count of files under "blobs/" via the Driver.
//     Inline manifests carry no separate blob file and do not appear.
//   - TotalBytes / UsedBytes / AvailableBytes are -1 ("unavailable"):
//     the Driver does not expose disk-free, and precise byte accounting
//     would need a full scan we do not want to run on Capacity.
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
	// system artifacts (name-addressed, not indexed), so store.config and
	// the future store.state writers do not inflate user-facing storage stats.
	var artifactCount int64
	if err := s.index.IterateManifests(ctx, func(domain.Manifest) error {
		artifactCount++
		return nil
	}); err != nil {
		return domain.StorageInfo{}, fmt.Errorf("store.Capacity: count manifests: %w", err)
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

	return out, nil
}

package core

// capacity.go — Store.Capacity. Aggregates index walks and driver
// counts; deliberately best-effort until StoreIndex grows a sized
// summary (a future milestone replaces the full index walk with a
// cached counter maintained on each IndexManifest call).

import (
	"context"
	"fmt"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

// Capacity returns aggregated storage info. Best-effort in M1.4:
//
//   - ArtifactCount: count of user-visible manifests, sourced from
//     the index walked with the "*" wildcard. system.* namespaces
//     are excluded — Capacity reports what users see through Walk,
//     not the raw on-disk manifest count.
//   - BlobCount: physical count of files under "blobs/" via the
//     Driver. Inline manifests carry no separate blob file and so
//     do not appear here.
//   - TotalBytes / UsedBytes / AvailableBytes are -1 (sentinel
//     "unavailable"). Driver does not expose disk-free; precise
//     byte accounting requires a full scan we do not want to do
//     on Capacity. Real numbers arrive in M2 once StoreIndex
//     grows a sized-summary method.
//
// The method honours ctx cancellation between the two operations.
// Offline Stores reject Capacity (operators can still inspect
// through State / Capabilities).
func (s *store) Capacity(ctx context.Context) (domain.StorageInfo, error) {
	if err := ctx.Err(); err != nil {
		return domain.StorageInfo{}, err
	}
	if s.maintenanceMode() == domain.MaintenanceModeOffline {
		return domain.StorageInfo{}, errs.ErrStoreOffline
	}

	out := domain.StorageInfo{
		TotalBytes:     -1,
		UsedBytes:      -1,
		AvailableBytes: -1,
	}

	// ArtifactCount: count of user-visible manifests. Walks the
	// index with the "*" wildcard, which already excludes system.*
	// namespaces (see index/sqlite ListByNamespace queryAny), so
	// system.config and the future system.state writers do not
	// inflate user-facing storage stats.
	var artifactCount int64
	if err := s.index.ListByNamespace(ctx, domain.NamespaceWildcard, func(domain.Manifest) error {
		artifactCount++
		return nil
	}); err != nil {
		return domain.StorageInfo{}, fmt.Errorf("core.Capacity: count manifests: %w", err)
	}
	out.ArtifactCount = artifactCount

	if err := ctx.Err(); err != nil {
		return domain.StorageInfo{}, err
	}

	// BlobCount: physical blobs/ count. Inline manifests (system.*
	// artifacts in M1.4) carry no separate blob file, so they do
	// not contribute here.
	blobs, err := s.drv.CountObjects(ctx, "blobs")
	if err != nil {
		return domain.StorageInfo{}, fmt.Errorf("core.Capacity: count blobs: %w", err)
	}
	out.BlobCount = blobs

	return out, nil
}

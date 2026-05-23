package orphanscan

import (
	"context"
	"errors"
	"fmt"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store/internal/blobpath"
)

// OrphanReport is the result of a RecoverOrphans pass. Counts are
// files actually removed; Errors collects non-fatal per-file failures
// that neither stop the scan nor block the Store from opening.
type OrphanReport struct {
	StagingRemoved   int
	BlobsRemoved     int
	ManifestsRemoved int
	Errors           []error
	Duration         time.Duration
}

// RecoverOrphans is a forward sweep of the filesystem against the
// index, removing three classes of physical orphan left by crashed
// writes:
//
//  1. staging/* — every file; staging is per-Put and per-process, so
//     anything that survived a restart is garbage from a crashed write.
//  2. blobs/<x>/<y>/<ref> — files whose ref does not resolve through
//     StoreIndex.Resolve (crash between Rename and IndexManifest).
//  3. manifests/<x>/<y>/<id> — files absent from the manifests table
//     (crash between the manifest Put and IndexManifest).
//
// The reverse sweep (index rows pointing at vanished files) is the
// rebuild agent's job, not this one. ListObjectsWithModTime treats a
// missing prefix as an empty walk, so this is safe to call after both
// InitStore and OpenStore.
func RecoverOrphans(ctx context.Context, drv driver.Driver, idx index.StoreIndex) (OrphanReport, error) {
	start := time.Now()
	report := OrphanReport{}

	// 1. Sweep system.state/staging/. Unconditional removal: any
	// file here is from a crashed prior process. Per-file Remove
	// errors do not stop the sweep.
	if err := drv.ListObjectsWithModTime(ctx, domain.StagingPrefix, time.Time{},
		func(om driver.ObjectMeta) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if rmErr := drv.Remove(ctx, om.Path); rmErr != nil {
				report.Errors = append(report.Errors,
					fmt.Errorf("recoverOrphans: staging remove %q: %w", om.Path, rmErr))
				return nil
			}
			report.StagingRemoved++
			return nil
		}); err != nil {
		// ctx.Err() or a List-level failure aborts the whole
		// recovery; partial reports are still useful so we
		// return what we have alongside the error.
		report.Duration = time.Since(start)
		return report, fmt.Errorf("recoverOrphans: staging sweep: %w", err)
	}

	// 2. Sweep blobs/. For every file, parse the blob ref out of
	// the last path segment and ask the index whether it knows
	// about it. errs.ErrArtifactNotFound — orphan, remove. Any other
	// error from Resolve is index infrastructure trouble; we log
	// and skip the file (better leave a possible orphan on disk
	// than mistake healthy data for orphan because of a transient
	// SQLite hiccup).
	if err := drv.ListObjectsWithModTime(ctx, "blobs", time.Time{},
		func(om driver.ObjectMeta) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			ref, err := blobpath.RefFromPath(om.Path)
			if err != nil {
				report.Errors = append(report.Errors,
					fmt.Errorf("recoverOrphans: blobs parse: %w", err))
				return nil
			}
			_, resolveErr := idx.Resolve(ctx, ref)
			switch {
			case errors.Is(resolveErr, errs.ErrArtifactNotFound):
				if rmErr := drv.Remove(ctx, om.Path); rmErr != nil {
					report.Errors = append(report.Errors,
						fmt.Errorf("recoverOrphans: blobs remove %q: %w", om.Path, rmErr))
					return nil
				}
				report.BlobsRemoved++
			case resolveErr != nil:
				report.Errors = append(report.Errors,
					fmt.Errorf("recoverOrphans: blobs resolve %q: %w", ref, resolveErr))
			}
			return nil
		}); err != nil {
		report.Duration = time.Since(start)
		return report, fmt.Errorf("recoverOrphans: blobs sweep: %w", err)
	}

	// 3. Sweep manifests/. Same shape as the blobs sweep, but
	// against ManifestExists rather than Resolve.
	if err := drv.ListObjectsWithModTime(ctx, "manifests", time.Time{},
		func(om driver.ObjectMeta) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, err := blobpath.ArtifactIDFromManifestPath(om.Path)
			if err != nil {
				report.Errors = append(report.Errors,
					fmt.Errorf("recoverOrphans: manifests parse: %w", err))
				return nil
			}
			exists, err := idx.ManifestExists(ctx, id)
			if err != nil {
				report.Errors = append(report.Errors,
					fmt.Errorf("recoverOrphans: manifests exists %q: %w", id, err))
				return nil
			}
			if exists {
				return nil
			}
			if rmErr := drv.Remove(ctx, om.Path); rmErr != nil {
				report.Errors = append(report.Errors,
					fmt.Errorf("recoverOrphans: manifests remove %q: %w", om.Path, rmErr))
				return nil
			}
			report.ManifestsRemoved++
			return nil
		}); err != nil {
		report.Duration = time.Since(start)
		return report, fmt.Errorf("recoverOrphans: manifests sweep: %w", err)
	}

	report.Duration = time.Since(start)
	return report, nil
}

// PublishOrphanReport emits EventOrphanScanCompleted when a Publisher
// is wired. The payload carries counts, not the error values
// themselves. No-op on a nil Publisher.
func PublishOrphanReport(pub event.Publisher, r OrphanReport) {
	if pub == nil {
		return
	}
	pub.Publish(event.Event{
		Type: event.EventOrphanScanCompleted,
		Payload: event.OrphanScanCompletedPayload{
			StagingRemoved:   r.StagingRemoved,
			BlobsRemoved:     r.BlobsRemoved,
			ManifestsRemoved: r.ManifestsRemoved,
			NonFatalErrors:   len(r.Errors),
			Duration:         r.Duration,
		},
	})
}

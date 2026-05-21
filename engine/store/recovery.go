package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/blobpath"
)

// OrphanReport is the result of a bootstrap recoverOrphans pass.
// Counts are files actually removed from disk; Errors collects
// non-fatal failures (per-file Driver.Remove rejections, parse
// glitches, individual Resolve/ManifestExists infrastructure
// errors) — none of these stop the scan or block the Store from
// opening.
type OrphanReport struct {
	StagingRemoved   int
	BlobsRemoved     int
	ManifestsRemoved int
	Errors           []error
	Duration         time.Duration
}

// recoverOrphans implements the M1 part of the Orphan Scan from
// docs/2 §10.2: a forward sweep of the filesystem against the
// index. Three classes of physical orphans are removed:
//
//  1. system.state/staging/* — every file. Staging is per-Put,
//     per-process; anything that survived a restart is by
//     construction garbage from a crashed prior write. Closes the
//     "partial blob write" Tier 1 scenario.
//
//  2. blobs/<x>/<y>/<ref> — files whose blob_ref does not resolve
//     through StoreIndex.Resolve. Closes the crash window between
//     Driver.Rename(staging→final) and IndexManifest in Put.
//
//  3. manifests/<x>/<y>/<id> — files whose artifact_id is not
//     present in the manifests table. Closes the crash window
//     between Driver.Put(manifestPath) and IndexManifest.
//
// The reverse sweep — index rows pointing at vanished files — is
// the M3 RebuildIndexAgent's job. In M1 every committed index row
// goes through IndexManifest's atomic SQLite transaction, so a
// row's existence implies its files were on disk at some point;
// the scenarios that produce reverse orphans (interrupted
// RebuildIndex, crash mid-Bundler, crash mid-Drain) all post-date
// M1.
//
// Driver.ListObjectsWithModTime swallows os.ErrNotExist for the
// prefix itself (an empty walk is the right answer for a fresh
// Store), so this function is safe to call after both InitStore
// (where blobs/ etc. do not exist yet) and OpenStore.
//
// TODO(M5): TOC integrity check (chunkRefs in blobs table per
// §10.2) lands with the chunker.Wrapper milestone; this function
// is the single place to add it when the TOC manifest type
// becomes reachable.
func recoverOrphans(ctx context.Context, drv driver.Driver, idx StoreIndex) (OrphanReport, error) {
	start := time.Now()
	report := OrphanReport{}

	// 1. Sweep system.state/staging/. Unconditional removal: any
	// file here is from a crashed prior process. Per-file Remove
	// errors do not stop the sweep.
	if err := drv.ListObjectsWithModTime(ctx, stagingPrefix, time.Time{},
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

// publishOrphanReport emits EventOrphanScanCompleted if a Publisher
// is wired. Counts are always emitted; individual error details
// stay in engine logs (the event payload carries a count, not the
// errors themselves — events should not transport mutable
// structures or platform-specific error types). Used by lifecycle
// after every successful recoverOrphans call.
func publishOrphanReport(pub Publisher, r OrphanReport) {
	if pub == nil {
		return
	}
	pub.Publish(event.Event{
		Type: EventOrphanScanCompleted,
		Payload: OrphanScanCompletedPayload{
			StagingRemoved:   r.StagingRemoved,
			BlobsRemoved:     r.BlobsRemoved,
			ManifestsRemoved: r.ManifestsRemoved,
			NonFatalErrors:   len(r.Errors),
			Duration:         r.Duration,
		},
	})
}

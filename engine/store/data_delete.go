package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// Delete logically removes an artifact from the Store. It does not free
// physical bytes — that is the GC Agent's job.
//
// Currently supported:
//   - BlobManifest only (TOC needs the chunker decorator to read chunk
//     refs from the TOC blob).
//   - Inline blobs are removed by deleting the manifest row alone —
//     there is no blobs row to decrement.
//   - Target blobs decrement the single ref_count.
//   - Pack manifests are invisible to clients and surface as
//     errs.ErrArtifactNotFound; GC touches them, not client Delete.
//
// Order of operations:
//  1. Load manifest.
//  2. Retention check — defends the artifact regardless of Store policy.
//  3. DeletionPolicy check — Store-level toggle.
//  4. StoreIndex.DeleteManifest — one transaction: decrement blob
//     ref_counts and remove the manifest row.
//  5. Driver.Remove(manifestPath).
//  6. EventArtifactDeleted — only after everything succeeded.
//
// A crash between (4) and (5) leaves an on-disk manifest with no index
// row; rebuilding the index from manifests is the recovery path.
func (d dataFacet) Delete(ctx context.Context, id domain.ArtifactID) error {
	if err := d.enterWrite(ctx); err != nil {
		return err
	}

	manifest, err := d.loadManifest(ctx, id)
	if err != nil {
		return err // errs.ErrArtifactNotFound or errs.ErrCorruptedManifest
	}

	// Handleless manifests are not user-visible (ADR-83).
	if err := guardHandleless(manifest); err != nil {
		return err
	}

	// Retention precedes policy: retention is artifact-level protection,
	// stronger than the Store-level policy. A retention-protected
	// artifact is refused before the policy is even consulted.
	if !manifest.RetentionUntil.IsZero() && manifest.RetentionUntil.After(time.Now()) {
		return errs.ErrRetentionNotExpired
	}

	cfg := d.snapshotConfig()
	if cfg.DeletionPolicy == domain.DeletionPolicyNoDelete {
		return errs.ErrDeletionForbidden
	}

	// Idempotent at this layer: a re-issued Delete after a crash between
	// index COMMIT and Driver.Remove would not reach here, because
	// loadManifest above would already have returned ErrArtifactNotFound
	// (the manifest file is gone). The "manifest present, index row
	// absent" window is recovered by an index rebuild. Deletion is keyed
	// by digest; the index derives the blobs to decrement from
	// manifest_blobs (Inline has no edges; Target has its one blob).
	if err := d.index.DeleteManifest(ctx, manifest.Digest); err != nil {
		return d.traceErr(ctx, "Delete", fmt.Errorf("store.Delete: index: %w", err), artifactIDAttr(id), slog.String("stage", "index"))
	}

	manifestPath, err := artifact.ManifestPath(manifest.Digest)
	if err != nil {
		return fmt.Errorf("store.Delete: manifest path: %w", err)
	}
	if err := d.drv.Remove(ctx, manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The index row is already gone, so the manifest file is now an
		// orphan that the GC Orphan Scan reaps on its next sweep. We still
		// surface the Remove error so the caller knows the operation was
		// not fully clean.
		return d.traceErr(ctx, "Delete", fmt.Errorf("store.Delete: remove manifest file: %w", err), artifactIDAttr(id), slog.String("stage", "remove"))
	}

	d.publish(event.EventArtifactDeleted, event.ArtifactDeletedPayload{ArtifactID: id})
	d.componentLogger("store").LogAttrs(ctx, slog.LevelDebug, "artifact deleted",
		storeIDAttr(d.core), artifactIDAttr(id),
		slog.String("blob_storage", manifest.LayoutHeader.BlobStorage))
	return nil
}

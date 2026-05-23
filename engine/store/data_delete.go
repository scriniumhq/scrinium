package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
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
func (s *store) Delete(ctx context.Context, id domain.ArtifactID) error {
	if err := s.enterWrite(ctx); err != nil {
		return err
	}

	manifest, err := s.loadManifest(ctx, id)
	if err != nil {
		return err // errs.ErrArtifactNotFound or errs.ErrCorruptedManifest
	}

	// Type dispatch.
	if err := dispatchManifestType(manifest, "store.Delete"); err != nil {
		return err
	}

	// Retention precedes policy: retention is artifact-level protection,
	// stronger than the Store-level policy. A retention-protected
	// artifact is refused before the policy is even consulted.
	if !manifest.RetentionUntil.IsZero() && manifest.RetentionUntil.After(time.Now()) {
		return errs.ErrRetentionNotExpired
	}

	cfg := s.snapshotConfig()
	if cfg.DeletionPolicy == domain.DeletionPolicyNoDelete {
		return errs.ErrDeletionForbidden
	}

	// Collect blobRefs to decrement. Inline = empty list (no row
	// in `blobs`). Target = the one BlobRef. ExternalRef would
	// also be empty, but Put rejects it today, so a manifest of
	// that layout cannot exist on disk yet — treat it as the
	// future-compatible empty list rather than special-casing.
	var blobRefs []string
	switch manifest.LayoutHeader.BlobStorage {
	case domain.LayoutInline:
		// no blobs row — leave blobRefs empty
	case domain.LayoutTarget:
		if manifest.BlobRef == "" {
			return fmt.Errorf("store.Delete: Target manifest %q has empty BlobRef", id)
		}
		blobRefs = []string{string(manifest.BlobRef)}
	case domain.LayoutExternalRef:
		// no blobs row by design
	default:
		return fmt.Errorf("store.Delete: unknown BlobStorage %q", manifest.LayoutHeader.BlobStorage)
	}

	// Idempotent at this layer: a re-issued Delete after a crash between
	// index COMMIT and Driver.Remove would not reach here, because
	// loadManifest above would already have returned ErrArtifactNotFound
	// (the manifest file is gone). The "manifest present, index row
	// absent" window is recovered by an index rebuild.
	if err := s.index.DeleteManifest(ctx, id, blobRefs); err != nil {
		return s.traceErr(ctx, "Delete", fmt.Errorf("store.Delete: index: %w", err), artifactIDAttr(id), slog.String("stage", "index"))
	}

	manifestPath, err := artifact.ManifestPath(id)
	if err != nil {
		return fmt.Errorf("store.Delete: manifest path: %w", err)
	}
	if err := s.drv.Remove(ctx, manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The index row is already gone, so the manifest file is now an
		// orphan that the GC Orphan Scan reaps on its next sweep. We still
		// surface the Remove error so the caller knows the operation was
		// not fully clean.
		return s.traceErr(ctx, "Delete", fmt.Errorf("store.Delete: remove manifest file: %w", err), artifactIDAttr(id), slog.String("stage", "remove"))
	}

	s.publish(event.EventArtifactDeleted, event.ArtifactDeletedPayload{ArtifactID: id})
	s.componentLogger("store").LogAttrs(ctx, slog.LevelDebug, "artifact deleted",
		storeIDAttr(s), artifactIDAttr(id),
		slog.String("blob_storage", manifest.LayoutHeader.BlobStorage))
	return nil
}

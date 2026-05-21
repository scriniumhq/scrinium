package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/blobpath"
)

// Delete logically removes an artifact from the Store. It does
// not free physical bytes — that is GC Agent territory (TODO M3,
// docs/2. Internals/05 §5.3). The flow is laid out in §2.2.
//
// Currently supported:
//   - BlobManifest only (TOC deferred to M5: requires reading the
//     TOC blob to gather chunk refs).
//   - Inline blobs are removed by deleting the manifest row
//     alone — there is no blobs row to decrement (§9.2.1).
//   - Target blobs decrement the single ref_count.
//   - Pack manifests are invisible to clients (§3.1) and surface
//     as errs.ErrArtifactNotFound; they would be touched by GC, not by
//     client Delete.
//
// Order of operations matches §2.2.Алгоритм:
//  1. Load manifest (Get's helper does it the same way).
//  2. Retention check — defends the artifact regardless of
//     Store policy.
//  3. DeletionPolicy check — Store-level toggle.
//  4. StoreIndex.DeleteManifest — single transaction, decrement
//     blob ref_counts, remove the manifest row.
//  5. Driver.Remove(manifestPath) — physical file gone.
//  6. EventArtifactDeleted — only after everything succeeded.
//
// Crash between (4) and (5) leaves an on-disk manifest with no
// index row. RebuildIndexAgent (TODO M3.4) is the recovery path.
func (s *store) Delete(ctx context.Context, id domain.ArtifactID) error {
	if err := s.enterWrite(ctx); err != nil {
		return err
	}

	manifest, err := s.loadManifest(ctx, id)
	if err != nil {
		return err // errs.ErrArtifactNotFound or errs.ErrCorruptedManifest
	}

	// Type dispatch.
	if err := dispatchManifestType(manifest, "core.Delete"); err != nil {
		return err
	}

	// Retention precedes policy: §2.2 explicitly orders these.
	// "Retention is artifact-level protection, stronger than the
	// Store-level policy" — so a NoDelete store can refuse a
	// non-retention artifact, but a retention-protected artifact
	// is refused before the policy is even consulted.
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
			return fmt.Errorf("core.Delete: Target manifest %q has empty BlobRef", id)
		}
		blobRefs = []string{string(manifest.BlobRef)}
	case domain.LayoutExternalRef:
		// no blobs row by design (§2.2)
	default:
		return fmt.Errorf("core.Delete: unknown BlobStorage %q", manifest.LayoutHeader.BlobStorage)
	}

	// Index transaction. Idempotent at this layer: a re-issued
	// Delete after a crash between index COMMIT and Driver.Remove
	// would not reach here because loadManifest above would
	// already have returned errs.ErrArtifactNotFound (the manifest
	// file is gone). The "manifest file present, index row
	// absent" window is RebuildIndexAgent territory.
	if err := s.index.DeleteManifest(ctx, id, blobRefs); err != nil {
		return fmt.Errorf("core.Delete: index: %w", err)
	}

	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return fmt.Errorf("core.Delete: manifest path: %w", err)
	}
	if err := s.drv.Remove(ctx, manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Index has already removed the row. The manifest file
		// is now an orphan; RebuildIndex won't help (no row to
		// reconstruct from), but GC's Orphan Scan in M3 will
		// reap it on next sweep. We still surface the Remove
		// error so the caller knows the operation was not
		// fully clean.
		return fmt.Errorf("core.Delete: remove manifest file: %w", err)
	}

	s.publish(event.EventArtifactDeleted, event.ArtifactDeletedPayload{ArtifactID: id})
	return nil
}

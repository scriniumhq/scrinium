package store

import (
	"context"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/cas"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// cas builds an cas.IO bound to this store's driver, index,
// and registries. The value is a cheap stateless handle, constructed per
// operation rather than held as a field.
func (c *core) cas() *cas.IO {
	return cas.New(c.drv, c.index, c.hashes, c.transformers)
}

// Get opens an artifact for reading. It reads only the manifest and
// prepares a ReadHandle; blob bytes stream lazily on the first
// Read/ReadAt. Inline blobs are served from memory; Target blobs are
// resolved through the index (not recomputed from the current topology)
// so the read path follows where the blob was actually written.
// VerifyOnRead may wrap the handle to re-check the content hash as bytes
// flow.
func (d dataFacet) Get(ctx context.Context, id domain.ArtifactID, opts ...domain.GetOption) (domain.ReadHandle, error) {
	if err := d.enterRead(ctx); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errs.ErrArtifactNotFound
	}

	manifest, err := d.loadManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := guardHandleless(manifest); err != nil {
		return nil, err
	}

	aio := d.cas()
	inner, err := aio.OpenHandle(ctx, manifest)
	if err != nil {
		return nil, err
	}

	// VerifyOnRead: empty-pipeline plain media is the case where the
	// engine is the only guard against silent bit rot. AEAD blobs and
	// media with native checksums auto-skip; ForceEnabled always wraps;
	// Disabled never does (see shouldVerifyOnRead).
	cfg := d.snapshotConfig()
	verify := shouldVerifyOnRead(cfg.VerifyOnRead, manifest.Pipeline, d.drv.Capabilities(), d.transformers)
	if log := d.componentLogger("store"); log.Enabled(ctx, slog.LevelDebug) {
		log.LogAttrs(ctx, slog.LevelDebug, "get opened",
			storeIDAttr(d.core), artifactIDAttr(id),
			slog.String("blob_storage", manifest.LayoutHeader.BlobStorage),
			slog.Bool("verify_on_read", verify))
	}
	if verify {
		return aio.WrapVerifying(inner, func(aid domain.ArtifactID, vErr error) {
			// Fires inside the caller's Read (after Get returned), so the
			// store could not observe vErr directly. Publish the scrub
			// event here; the error itself still propagates to the reader.
			d.publish(event.EventScrubFailed, event.ScrubFailedPayload{ArtifactID: aid, Err: vErr})
		})
	}
	return inner, nil
}

// loadManifest reads, verifies, and decodes the manifest file for id via
// the artifact I/O layer. Used by Get, Delete, and Verify. Returns
// ErrArtifactNotFound when the file is absent and ErrCorruptedManifest
// when its hash does not match id. State checks are the caller's job.
//
// Decode dispatches on the file header: Plain bypasses the resolver;
// encrypted (Sealed/Paranoid) consults the snapshotted resolver. A Locked
// Store has a nil resolver, which surfaces ErrKeyNotFound — the correct
// refusal. asKeyProvider maps a nil resolver to a nil provider (the
// typed-nil guard).
func (c *core) loadManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, error) {
	return c.cas().Load(ctx, id, c.crypto.KeyProvider(), string(c.snapshotConfig().ContentHasher))
}

// guardHandleless enforces the negative identity invariant (ADR-83): a
// manifest with an empty identity slot (handle IS NULL) is not a
// user-visible artifact — a pack container or other engine-internal
// object — so user-facing Get/Delete/Verify collapse it to not-found
// rather than leaking it. Structure (chunked/composite bodies) is no
// longer dispatched here: the owning wrapper handles it (ADR-88), and a
// body whose layout needs an absent decorator fails in the open path.
func guardHandleless(m domain.Manifest) error {
	if !m.IsUser() {
		return errs.ErrArtifactNotFound
	}
	return nil
}

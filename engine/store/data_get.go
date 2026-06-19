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
func (s *store) cas() *cas.IO {
	return cas.New(s.drv, s.index, s.hashes, s.transformers)
}

// Get opens an artifact for reading. It reads only the manifest and
// prepares a ReadHandle; blob bytes stream lazily on the first
// Read/ReadAt. Inline blobs are served from memory; Target blobs are
// resolved through the index (not recomputed from the current topology)
// so the read path follows where the blob was actually written.
// VerifyOnRead may wrap the handle to re-check the content hash as bytes
// flow.
func (s *store) Get(ctx context.Context, id domain.ArtifactID, opts ...domain.GetOption) (domain.ReadHandle, error) {
	if err := s.enterRead(ctx); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errs.ErrArtifactNotFound
	}

	manifest, err := s.loadManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := guardHandleless(manifest); err != nil {
		return nil, err
	}

	aio := s.cas()
	inner, err := aio.OpenHandle(ctx, manifest)
	if err != nil {
		return nil, err
	}

	// VerifyOnRead: empty-pipeline plain media is the case where the
	// engine is the only guard against silent bit rot. AEAD blobs and
	// media with native checksums auto-skip; ForceEnabled always wraps;
	// Disabled never does (see shouldVerifyOnRead).
	cfg := s.snapshotConfig()
	verify := shouldVerifyOnRead(cfg.VerifyOnRead, manifest.Pipeline, s.drv.Capabilities(), s.transformers)
	if log := s.componentLogger("store"); log.Enabled(ctx, slog.LevelDebug) {
		log.LogAttrs(ctx, slog.LevelDebug, "get opened",
			storeIDAttr(s), artifactIDAttr(id),
			slog.String("blob_storage", manifest.LayoutHeader.BlobStorage),
			slog.Bool("verify_on_read", verify))
	}
	if verify {
		return aio.WrapVerifying(inner, func(aid domain.ArtifactID, vErr error) {
			// Fires inside the caller's Read (after Get returned), so the
			// store could not observe vErr directly. Publish the scrub
			// event here; the error itself still propagates to the reader.
			s.publish(event.EventScrubFailed, event.ScrubFailedPayload{ArtifactID: aid, Err: vErr})
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
func (s *store) loadManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, error) {
	return s.cas().Load(ctx, id, s.crypto.KeyProvider(), string(s.snapshotConfig().ContentHasher))
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

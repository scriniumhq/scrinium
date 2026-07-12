package store

import (
	"context"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

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

	aio := s.contentIO()
	inner, err := aio.OpenHandle(ctx, manifest)
	if err != nil {
		return nil, err
	}

	// VerifyOnRead: empty-pipeline plain media is the case where the
	// engine is the only guard against silent bit rot. AEAD blobs and
	// media with native checksums auto-skip; ForceEnabled always wraps;
	// Disabled never does (see shouldVerifyOnRead).
	// Session-effective config (ADR-110): Get consumes class-III
	// fields (VerifyOnRead, EagerFetchLimit).
	cfg := s.sessionConfig()
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

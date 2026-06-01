package store

import (
	"context"
	"fmt"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/artifactio"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// artifactIO builds an artifactio.IO bound to this store's driver, index,
// and registries. The value is a cheap stateless handle, constructed per
// operation rather than held as a field.
func (c *core) artifactIO() *artifactio.IO {
	return artifactio.New(c.drv, c.index, c.hashes, c.transformers)
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
	if err := dispatchManifestType(manifest, "store.Get"); err != nil {
		return nil, err
	}

	aio := d.artifactIO()
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
	return c.artifactIO().Load(ctx, id, asKeyProvider(c.crypto.resolver()))
}

// dispatchManifestType returns nil for a regular Blob manifest, or the
// right sentinel otherwise. Get, Delete, and Verify share it: Blob
// continues, TOC awaits the chunker decorator, Pack is engine-internal
// (surfaced as not-found), anything else is unknown. op names the
// operation for the error message.
func dispatchManifestType(m domain.Manifest, op string) error {
	switch m.Type {
	case domain.ManifestTypeBlob:
		return nil
	case domain.ManifestTypeTOC:
		return fmt.Errorf("%w: %s on ManifestTypeTOC requires the chunker decorator", errs.ErrNotImplemented, op)
	case domain.ManifestTypePack:
		// Pack manifests are engine-internal; collapse to not-found so
		// clients need not special-case them.
		return errs.ErrArtifactNotFound
	default:
		return fmt.Errorf("%s: unknown manifest type %q", op, m.Type)
	}
}

// asKeyProvider adapts a pipeline.KeyResolver to an artifact.KeyProvider,
// mapping a nil resolver to a nil provider. This avoids the typed-nil
// trap: a nil resolver passed straight into an interface parameter would
// become a non-nil interface value, and the codec's `keys == nil` check
// would miss it. "No resolver" must mean "no provider" — Plain manifests
// need none; encrypted ones then surface ErrKeyNotFound.
func asKeyProvider(r pipeline.KeyResolver) artifact.KeyProvider {
	if r == nil {
		return nil
	}
	return r
}

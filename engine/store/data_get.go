package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/blobpath"
	"scrinium.dev/engine/store/internal/manifestcodec"
)

// Get opens an artifact for reading. It reads only the manifest and
// prepares a ReadHandle; blob bytes stream lazily on the first
// Read/ReadAt. Inline blobs are served from memory; Target blobs are
// resolved through the index (not recomputed from the current
// topology) so the read path follows where the blob was actually
// written. VerifyOnRead may wrap the handle to re-check the content
// hash as bytes flow.
func (s *store) Get(ctx context.Context, id domain.ArtifactID, opts domain.GetOptions) (ReadHandle, error) {
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
	if err := dispatchManifestType(manifest, "store.Get"); err != nil {
		return nil, err
	}

	inner, err := s.openReadHandle(ctx, manifest)
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
		return newVerifyingReadHandle(inner, s)
	}
	return inner, nil
}

// openReadHandle builds the layout-appropriate ReadHandle for a
// dispatched Blob manifest.
func (s *store) openReadHandle(ctx context.Context, manifest domain.Manifest) (ReadHandle, error) {
	switch manifest.LayoutHeader.BlobStorage {
	case domain.LayoutInline:
		return &inlineReadHandle{manifest: manifest, reader: bytes.NewReader(manifest.InlineBlob)}, nil

	case domain.LayoutTarget:
		addr, err := s.index.Resolve(ctx, string(manifest.BlobRef))
		if err != nil {
			return nil, fmt.Errorf("store.Get: resolve blob path: %w", err)
		}
		return &targetReadHandle{
			manifest: manifest,
			drv:      s.drv,
			blobPath: addr.Path,
			ctx:      ctx,
			store:    s,
		}, nil

	case domain.LayoutExternalRef:
		return nil, fmt.Errorf("%w: store.Get on BlobStorage=ExternalRef awaits driver.Open URI dispatch", errs.ErrNotImplemented)

	default:
		return nil, fmt.Errorf("store.Get: unknown BlobStorage %q", manifest.LayoutHeader.BlobStorage)
	}
}

// loadManifest reads, verifies, and decodes the manifest file for id.
// Used by Get, Delete, and Verify. Returns ErrArtifactNotFound when
// the file is absent and ErrCorruptedManifest when its hash does not
// match id. State checks are the caller's job.
func (s *store) loadManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, error) {
	if id == "" {
		return domain.Manifest{}, errs.ErrArtifactNotFound
	}
	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("loadManifest: path: %w", err)
	}
	rc, err := s.drv.Get(ctx, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Manifest{}, errs.ErrArtifactNotFound
		}
		return domain.Manifest{}, fmt.Errorf("loadManifest: read: %w", err)
	}
	raw, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("loadManifest: read body: %w", err)
	}
	if err := manifestcodec.VerifyArtifactID(id, raw, s.hashes); err != nil {
		return domain.Manifest{}, err
	}

	// Decode dispatches on the file header: Plain bypasses the
	// resolver; encrypted (Sealed/Paranoid) consults the snapshotted
	// resolver. A Locked Store has a nil resolver, which surfaces
	// ErrKeyNotFound from the codec — the correct refusal.
	keyResolver := s.crypto.resolver()
	manifest, err := manifestcodec.DecodeFileEncrypted(raw, asKeyProvider(keyResolver))
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("loadManifest: decode: %w", err)
	}
	manifest.ArtifactID = id
	return manifest, nil
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

// asKeyProvider adapts a pipeline.KeyResolver to a
// manifestcodec.KeyProvider, mapping a nil resolver to a nil provider.
// This avoids the typed-nil trap: a nil resolver passed straight into
// an interface parameter would become a non-nil interface value, and
// the codec's `keys == nil` check would miss it. "No resolver" must
// mean "no provider" — Plain manifests need none; encrypted ones then
// surface ErrKeyNotFound.
func asKeyProvider(r pipeline.KeyResolver) manifestcodec.KeyProvider {
	if r == nil {
		return nil
	}
	return r
}

// parseContentHash splits a ContentHash into a fresh hasher and the
// expected digest. Used by every integrity path (Verify, the
// VerifyOnRead wrapper, and later Scrub). The algorithm comes from the
// ContentHash prefix, not the current config, so artifacts written
// under a previous hasher still validate after a migration. Callers
// stream plaintext through hasher and compare hasher.Sum(nil) to want.
func (s *store) parseContentHash(ch domain.ContentHash) (algo string, want []byte, hasher hash.Hash, err error) {
	algo, want, err = s.hashes.Parse(string(ch))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse ContentHash: %w", err)
	}
	hasher, err = s.hashes.NewHasher(algo)
	if err != nil {
		return "", nil, nil, fmt.Errorf("hasher %q: %w", algo, err)
	}
	return algo, want, hasher, nil
}

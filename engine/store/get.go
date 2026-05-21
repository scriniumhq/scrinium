package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
)

// asKeyProvider converts a store.KeyResolver into a
// manifestcodec.KeyProvider, taking care of the typed-nil trap:
// passing a nil *staticKeyResolver to anj*&$m$Pp61*BkoH8 interface parameter
// produces a non-nil interface value (with a type but no data),
// and DecodeFileEncrypted's `if keys == nil` would miss it.
// Treating "nil resolver" as "no provider" mirrors the spec:
// Plain manifests don't need a resolver, encrypted ones surface
// ErrKeyNotFound.
func asKeyProvider(r coreapi.KeyResolver) manifestcodec.KeyProvider {
	if r == nil {
		return nil
	}
	return r
}

// loadManifest reads, verifies, and decodes the manifest file for
// the given ArtifactID. Used by Get and Delete. Returns
// errs.ErrArtifactNotFound if the manifest file is absent on disk and
// errs.ErrCorruptedManifest if the file's hash does not match id.
//
// Caller is responsible for any state checks (checkOperational /
// checkWritable) — this helper is purely about disk → in-memory
// manifest conversion.
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

	// Decode dispatches on the file header: Plain bypass any
	// resolver, encrypted (Sealed / Paranoid) consult the
	// snapshotted keyResolver. The snapshot is taken under
	// cryptoMu and held only across the pointer copy — the
	// resolver itself is an immutable interface, no deeper copy
	// needed. A Locked Store has keyResolver == nil; for an
	// encrypted manifest that surfaces ErrKeyNotFound from the
	// codec, which is the correct refusal.
	s.cryptoMu.Lock()
	keyResolver := s.keyResolver
	s.cryptoMu.Unlock()

	manifest, err := manifestcodec.DecodeFileEncrypted(raw, asKeyProvider(keyResolver))
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("loadManifest: decode: %w", err)
	}
	manifest.ArtifactID = id
	return manifest, nil
}

// Get opens an artifact for reading. The call itself reads only
// the manifest file and prepares a ReadHandle; the blob bytes
// stream lazily on the first Read/ReadAt (docs/2. Internals/02 §2.4).
//
// M1.4 perimeter: BlobManifest only; Inline and Target layouts;
// no Pipeline (so no inverse decoder chain); no encryption (so
// no KeyResolver lookup); no Curator routing (opts.AllowColdRead
// ignored — it is a Curator-layer flag). TOC, Pack, ExternalRef,
// Sealed/Paranoid crypto are deferred to later milestones
// and return explicit errors when reached.
func (s *store) Get(ctx context.Context, id domain.ArtifactID, opts domain.GetOptions) (coreapi.ReadHandle, error) {
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

	// 4. Type dispatch.
	if err := dispatchManifestType(manifest, "store.Get"); err != nil {
		return nil, err
	}

	// 5. Layout dispatch (BlobManifest only).
	var inner coreapi.ReadHandle
	switch manifest.LayoutHeader.BlobStorage {
	case domain.LayoutInline:
		// Bytes already in memory inside the manifest. No driver
		// call; the handle is a thin wrapper around bytes.Reader.
		inner = &inlineReadHandle{
			manifest: manifest,
			reader:   bytes.NewReader(manifest.InlineBlob),
		}

	case domain.LayoutTarget:
		// PhysicalAddress is sourced from the index — the
		// authoritative cache populated at IndexManifest time.
		// Read-path does not recompute the path from the current
		// PathTopology: per the layout invariant (Internals/01),
		// manifests carry no placement data, and the path under
		// which a blob was actually written is what the index
		// records. This makes future layout changes (Reshuffle
		// Agent, OQ-21) safe by construction — the read-path
		// follows whatever the index says, the topology config
		// only governs where new writes go.
		addr, err := s.index.Resolve(ctx, string(manifest.BlobRef))
		if err != nil {
			return nil, fmt.Errorf("store.Get: resolve blob path: %w", err)
		}
		inner = &targetReadHandle{
			manifest: manifest,
			drv:      s.drv,
			blobPath: addr.Path,
			ctx:      ctx,
			store:    s,
		}

	case domain.LayoutExternalRef:
		return nil, fmt.Errorf("%w: store.Get on BlobStorage=ExternalRef awaits driver.Open URI dispatch", errs.ErrNotImplemented)

	default:
		return nil, fmt.Errorf("store.Get: unknown BlobStorage %q", manifest.LayoutHeader.BlobStorage)
	}

	// 6. VerifyOnRead policy.
	//
	// Empty pipeline + plain media is the canonical case where the
	// engine itself is the only line of defence against silent bit
	// rot; AEAD-protected blobs and media with native checksums are
	// auto-skipped (see shouldVerifyOnRead). ForceEnabled wraps
	// unconditionally; Disabled skips even on plain media.
	cfg := s.snapshotConfig()
	if shouldVerifyOnRead(cfg.VerifyOnRead, manifest.Pipeline, s.drv.Capabilities(), s.transformers) {
		wrapped, err := newVerifyingReadHandle(inner, s)
		if err != nil {
			return nil, err
		}
		return wrapped, nil
	}
	return inner, nil

	// no Curator routing (opts.AllowColdRead is a Curator-layer flag
	// per docs/4. API Reference/03 §3.1 — without a Curator it has no
	// effect; the argument is accepted for ABI compatibility and
	// otherwise ignored).
}

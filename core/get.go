package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/blobpath"
	"github.com/rkurbatov/scrinium/internal/manifestcodec"
)

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
	manifest, err := manifestcodec.DecodeFile(raw)
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
// MetadataOnly/Envelope crypto are deferred to later milestones
// and return explicit errors when reached.
func (s *store) Get(ctx context.Context, id domain.ArtifactID, opts GetOptions) (ReadHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkOperational(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errs.ErrArtifactNotFound
	}

	cfg := s.snapshotConfig()

	manifest, err := s.loadManifest(ctx, id)
	if err != nil {
		return nil, err
	}

	// 4. Type dispatch.
	switch manifest.Type {
	case domain.ManifestTypeBlob:
		// continue below
	case domain.ManifestTypeTOC:
		return nil, fmt.Errorf("core.Get: ManifestTypeTOC deferred to M5")
	case domain.ManifestTypePack:
		// §3.1: pack manifests are engine-internal, invisible to
		// clients. We collapse them into "not found" so client
		// code does not have to special-case them.
		return nil, errs.ErrArtifactNotFound
	default:
		return nil, fmt.Errorf("core.Get: unknown manifest type %q", manifest.Type)
	}

	// 5. Layout dispatch (BlobManifest only).
	switch manifest.LayoutHeader.BlobStorage {
	case "Inline":
		// Bytes already in memory inside the manifest. No driver
		// call; the handle is a thin wrapper around bytes.Reader.
		return &inlineReadHandle{
			manifest: manifest,
			reader:   bytes.NewReader(manifest.InlineBlob),
		}, nil

	case "Target":
		// PathTopology comes from current StoreConfig, not the
		// manifest. This is intentional: the manifest does not
		// encode a "where on disk" — that is a Store-wide concern.
		// A Store that changes PathTopology mid-life would break
		// historical reads; that migration is MigrateIndexAgent's
		// job in M3.
		blobPath, err := blobpath.Resolve(cfg.PathTopology, domain.BlobTypeRegular, string(manifest.BlobRef))
		if err != nil {
			return nil, fmt.Errorf("core.Get: resolve blob path: %w", err)
		}
		return &targetReadHandle{
			manifest: manifest,
			drv:      s.drv,
			blobPath: blobPath,
			ctx:      ctx,
		}, nil

	case "ExternalRef":
		return nil, fmt.Errorf("core.Get: BlobStorage: ExternalRef deferred to a later milestone")

	default:
		return nil, fmt.Errorf("core.Get: unknown BlobStorage %q", manifest.LayoutHeader.BlobStorage)
	}

	// no Curator routing (opts.AllowColdRead is a Curator-layer flag
	// per §3.1.Параметры — without a Curator it has no effect; the
	// argument is accepted for ABI compatibility and otherwise ignored).
}

// --- inlineReadHandle: bytes live in the manifest itself ---

type inlineReadHandle struct {
	manifest domain.Manifest
	reader   *bytes.Reader
}

func (h *inlineReadHandle) Read(p []byte) (int, error) {
	return h.reader.Read(p)
}

func (h *inlineReadHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.reader.ReadAt(p, off)
}

func (h *inlineReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.reader.ReadAt(p, off)
}

func (h *inlineReadHandle) SupportsRandomAccess() bool {
	return true
}

func (h *inlineReadHandle) Close() error {
	// In-memory; nothing to release. Idempotent.
	return nil
}

func (h *inlineReadHandle) Manifest() domain.Manifest {
	return h.manifest
}

// --- targetReadHandle: blob is a separate file under /blobs/ ---
//
// The handle defers opening the blob until the first Read. ReadAt
// always opens a fresh range descriptor through Driver.ReadAt and
// closes it before returning — that is the simplest way to layer
// random access over a streaming Driver.Get without sharing state
// with the linear Read cursor.

type targetReadHandle struct {
	manifest domain.Manifest
	drv      driver.Driver
	blobPath string
	// ctx captured at Get-time; used for non-Ctx Read/ReadAt that
	// have no ambient context per the io.Reader/io.ReaderAt
	// contracts. ReadAtCtx ignores this field and uses its own.
	ctx context.Context

	mu     sync.Mutex
	rc     io.ReadCloser // lazily opened on first Read
	closed bool
}

func (h *targetReadHandle) Read(p []byte) (int, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, os.ErrClosed
	}
	if h.rc == nil {
		rc, err := h.drv.Get(h.ctx, h.blobPath)
		if err != nil {
			h.mu.Unlock()
			if errors.Is(err, os.ErrNotExist) {
				// Manifest references a blob that is not on disk.
				// The classical Scrub-failure case from §3.1.Эффекты;
				// surfacing as errs.ErrCorruptedBlob lets callers
				// distinguish "wrong id" (errs.ErrArtifactNotFound at Get)
				// from "blob missing" (here, during Read).
				return 0, errs.ErrCorruptedBlob
			}
			return 0, err
		}
		h.rc = rc
	}
	rc := h.rc
	h.mu.Unlock()
	return rc.Read(p)
}

func (h *targetReadHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.readAt(h.ctx, p, off)
}

func (h *targetReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	return h.readAt(ctx, p, off)
}

func (h *targetReadHandle) readAt(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, os.ErrClosed
	}
	h.mu.Unlock()

	rc, err := h.drv.ReadAt(ctx, h.blobPath, off, int64(len(p)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, errs.ErrCorruptedBlob
		}
		return 0, err
	}
	defer rc.Close()
	n, readErr := io.ReadFull(rc, p)
	// io.ReaderAt contract: when n < len(p), err must be non-nil
	// and is typically io.EOF for short reads. ReadFull returns
	// ErrUnexpectedEOF in that case; convert to EOF for the
	// caller.
	if errors.Is(readErr, io.ErrUnexpectedEOF) {
		readErr = io.EOF
	}
	return n, readErr
}

func (h *targetReadHandle) SupportsRandomAccess() bool {
	// Target blobs on a localfs-style driver always support
	// random access. When pipelines arrive in M2+ this will need
	// to consult the manifest.Pipeline composition (a streaming
	// decompressor disables ReadAt) — for now Pipeline is empty.
	return true
}

func (h *targetReadHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil // idempotent
	}
	h.closed = true
	if h.rc == nil {
		return nil
	}
	err := h.rc.Close()
	h.rc = nil
	return err
}

func (h *targetReadHandle) Manifest() domain.Manifest {
	return h.manifest
}

// Compile-time interface conformance.
var (
	_ ReadHandle = (*inlineReadHandle)(nil)
	_ ReadHandle = (*targetReadHandle)(nil)
)

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/event"
	"github.com/rkurbatov/scrinium/internal/blobpath"
	"github.com/rkurbatov/scrinium/internal/manifestcodec"
)

// stagingPrefix is the directory where in-flight blob writes live
// until their content hash is known. After the hash is computed
// the file is renamed to its final hash-derived path.
//
// Living under system.state keeps staging blobs out of the way of
// all other engine code: the Recovery Agent in M3 will treat
// dangling staging files as orphans and prune them. We do not
// need a HostStorage transit buffer for this: every staging blob
// is rewritten or removed within a single Put call.
const stagingPrefix = "system.state/staging"

// Put records an artifact in the Store. M1.4 implements the
// Target-with-managed-blob path:
//
//   - the payload is hashed while streaming to a staging file,
//   - dedup is checked by ContentHash + size,
//   - on a fresh blob the staging file is renamed to its final
//     hash-derived path,
//   - on a hit the staging file is removed; the existing BlobRef
//     is reused.
//
// Then the manifest is built, hashed (becomes ArtifactID), and
// written to its own hash-sharded path under manifests/. Finally
// the index is updated.
//
// Inline-blob, ExternalRef, Pipeline, Encryption, HostStorage
// transit, and Pack volumes are deferred to later milestones.
// Reaching a code path that needs them returns an explicit error.
func (s *store) Put(ctx context.Context, a domain.Artifact, opts PutOptions) (domain.ArtifactID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkWritable(); err != nil {
		return "", err
	}
	if err := validatePutInputs(a, opts); err != nil {
		return "", err
	}

	cfg := s.snapshotConfig()

	// M1.4 perimeter: bail out on the surfaces that are stubbed.
	if opts.BlobType != "" && opts.BlobType != BlobTypeRegular {
		return "", fmt.Errorf("core.Put: BlobType %q deferred to M3", opts.BlobType)
	}
	if cfg.BlobStorage == domain.BlobStorageExternalRef {
		return "", errors.New("core.Put: BlobStorage: ExternalRef deferred to a later milestone")
	}
	if cfg.BlobStorage == domain.BlobStorageInlineFallback {
		return "", errors.New("core.Put: BlobStorage: Inline deferred to M1.4 pack 3")
	}
	if cfg.ManifestStorage != domain.ManifestStorageRemote && cfg.ManifestStorage != "" {
		return "", fmt.Errorf("core.Put: ManifestStorage %q deferred to M2.2",
			cfg.ManifestStorage)
	}

	// --- Phase 1: stream payload to staging, hash it ---

	hashAlgo := string(cfg.ContentHasher)
	hasher, err := s.hashes.NewHasher(hashAlgo)
	if err != nil {
		return "", fmt.Errorf("core.Put: hasher: %w", err)
	}

	stagingPath, err := s.makeStagingPath()
	if err != nil {
		return "", err
	}
	tee := io.TeeReader(a.Payload, hasher)
	counter := &countingReader{r: tee}

	if err := s.drv.Put(ctx, stagingPath, counter); err != nil {
		// Driver.Put atomically rejects the staging write; nothing
		// to clean up.
		return "", fmt.Errorf("core.Put: stage payload: %w", err)
	}

	contentHash := domain.ContentHash(s.hashes.Format(hashAlgo, hasher.Sum(nil)))
	originalSize := counter.n

	// --- Phase 2: dedup check ---

	existingRef, found, err := s.index.ExistsByContent(contentHash, originalSize)
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", fmt.Errorf("core.Put: dedup probe: %w", err)
	}

	var (
		blobRef  domain.BlobRef
		blobAddr PhysicalAddress
	)
	if found {
		// Dedup hit: reuse the existing blob, drop the staging file.
		if err := s.drv.Remove(ctx, stagingPath); err != nil {
			return "", fmt.Errorf("core.Put: drop staging: %w", err)
		}
		blobRef = domain.BlobRef(existingRef)
		blobAddr, err = s.index.Resolve(existingRef)
		if err != nil {
			return "", fmt.Errorf("core.Put: resolve existing blob: %w", err)
		}
	} else {
		// Fresh blob: BlobRef equals ContentHash in M1.4 because
		// Pipeline is empty (no transformations between content
		// and on-disk bytes). When the Pipeline lands in M2,
		// BlobRef becomes the hash of the post-pipeline stream.
		blobRef = domain.BlobRef(contentHash)
		finalPath, err := blobpath.Resolve(cfg.PathTopology, domain.BlobTypeRegular, string(blobRef))
		if err != nil {
			_ = s.drv.Remove(ctx, stagingPath)
			return "", fmt.Errorf("core.Put: resolve blob path: %w", err)
		}
		if err := s.drv.Rename(ctx, stagingPath, finalPath); err != nil {
			_ = s.drv.Remove(ctx, stagingPath)
			return "", fmt.Errorf("core.Put: commit blob: %w", err)
		}
		blobAddr = PhysicalAddress{
			Workspace: WorkspaceLocation,
			Path:      finalPath,
		}
	}

	// --- Phase 3: build manifest and compute its ArtifactID ---

	createdAt := time.Now().UTC()
	manifest := domain.Manifest{
		Type:           domain.ManifestTypeBlob,
		Namespace:      opts.Namespace,
		SessionID:      opts.SessionID,
		CreatedAt:      createdAt,
		ContentHash:    contentHash,
		OriginalSize:   originalSize,
		BlobRef:        blobRef,
		LayoutHeader:   domain.LayoutHeader{BlobStorage: string(domain.BlobStorageTarget)},
		Pipeline:       []domain.PipelineStage{},
		RetentionUntil: opts.RetentionUntil,
		Metadata:       a.Metadata,
	}
	artifactID, manifestBytes, manifest, err := manifestcodec.ComputeArtifactID(
		manifest, hashAlgo, s.hashes,
		cfg.ManifestEncoding, cfg.ManifestCrypto,
	)
	if err != nil {
		// On an encoding/crypto deferral the blob is already
		// committed. We do NOT roll it back: the orphan blob is
		// harmless (ref_count stays 0, GC reaps it). Rolling back
		// would require an inverse of Driver.Rename, which can race
		// against a parallel Put deduping on the same content.
		return "", fmt.Errorf("core.Put: compute artifact id: %w", err)
	}

	// --- Phase 4: write the manifest file ---

	manifestPath, err := blobpath.ManifestPath(artifactID)
	if err != nil {
		return "", fmt.Errorf("core.Put: manifest path: %w", err)
	}
	if err := s.drv.Put(ctx, manifestPath, bytesReader(manifestBytes)); err != nil {
		return "", fmt.Errorf("core.Put: write manifest: %w", err)
	}

	// --- Phase 5: index ---

	if err := s.index.IndexManifest(manifest, blobAddr, nil, nil); err != nil {
		// Manifest file is on disk but unindexed. RebuildIndexAgent
		// (M3) is the recovery path. We surface the error so the
		// caller can retry the index step or reissue Put (which
		// will dedup the blob and re-attempt the manifest).
		return "", fmt.Errorf("core.Put: index manifest: %w", err)
	}

	// --- Phase 6: emit ---

	s.publish(EventManifestSaved, ManifestSavedPayload{
		Manifest:  manifest,
		IsTransit: false,
	})

	return artifactID, nil
}

// snapshotConfig returns a copy of the current active StoreConfig.
// Cheap because StoreConfig is value-typed; the lock is held only
// for the duration of the copy.
func (s *store) snapshotConfig() domain.StoreConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.activeConfig
}

// checkWritable extends checkOperational with the ReadOnly check.
// Used at the entry of every mutating method; read-only operations
// (Walk, Capacity, Get) use checkOperational alone.
func (s *store) checkWritable() error {
	if err := s.checkOperational(); err != nil {
		return err
	}
	if s.maintenanceMode() == MaintenanceModeReadOnly {
		return ErrStoreReadOnly
	}
	return nil
}

// validatePutInputs covers the cheap, side-effect-free checks that
// must reject before any I/O. Order matches the priority of
// docs/2. Internals/01 §1.4.
func validatePutInputs(a domain.Artifact, opts PutOptions) error {
	if a.Payload == nil && opts.ExternalURI == "" {
		return errors.New("core.Put: nil Payload and no ExternalURI")
	}
	if len(opts.Namespace) > 255 {
		return domain.ErrNamespaceTooLong
	}
	if strings.HasPrefix(opts.Namespace, "system.") || opts.Namespace == "*" {
		return ErrReservedNamespace
	}
	if len(opts.SessionID) > 255 {
		return domain.ErrSessionIDTooLong
	}
	if len(a.Metadata) > 64*1024 {
		return domain.ErrMetadataTooLarge
	}
	return nil
}

// publish emits an event when a Publisher is configured. Cheap when
// nil — the common case for tests and minimal-stack hosts.
func (s *store) publish(typ string, payload any) {
	if s.pub == nil {
		return
	}
	s.pub.Publish(event.Event{Type: typ, Payload: payload})
}

// makeStagingPath returns a fresh, unique path under
// system.state/staging/. Uniqueness is provided by the UUID v4
// helper (the same generator we use for StoreID). A future
// improvement (multi-host) is to mix in a host_id; M3.1 territory.
func (s *store) makeStagingPath() (string, error) {
	id, err := generateStoreID()
	if err != nil {
		return "", fmt.Errorf("core.Put: staging id: %w", err)
	}
	return stagingPrefix + "/" + id, nil
}

// countingReader wraps an io.Reader and tracks the number of bytes
// passed through. We need OriginalSize and the hash in one stream;
// a TeeReader gives us the hash, but the byte count comes from
// here.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// bytesReader wraps a byte slice into an io.Reader. Reused from
// the descriptor package's pattern: avoids importing bytes only
// for the one-shot reader.
func bytesReader(b []byte) io.Reader {
	return &sliceReader{b: b}
}

type sliceReader struct {
	b   []byte
	off int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

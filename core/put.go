package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
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

// Put records an artifact in the Store. M1.4 implements two
// blob-placement paths:
//
//	Target: payload streams to a staging file, content is hashed
//	on the fly, dedup is checked, the staging file is renamed to
//	its final hash-derived path.
//
//	Inline (chosen when StoreConfig.BlobStorage is InlineFallback
//	AND len(payload) <= InlineBlobLimit): payload is buffered in
//	memory and stored inside the manifest. No blob file is
//	produced; dedup is disabled because inline bytes have no
//	separate identity in the blobs table (docs §… "Deduplication
//	is forcibly disabled" for inline blobs).
//
// Then the manifest is built, hashed (becomes ArtifactID), and
// written to its own hash-sharded path under manifests/. Finally
// the index is updated.
//
// ExternalRef, Pipeline, Encryption, HostStorage transit, and
// Pack volumes are deferred to later milestones. Reaching a code
// path that needs them returns an explicit error.
func (s *store) Put(ctx context.Context, a domain.Artifact, opts domain.PutOptions) (domain.ArtifactID, error) {
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
	if opts.BlobType != "" && opts.BlobType != domain.BlobTypeRegular {
		return "", fmt.Errorf("core.Put: BlobType %q deferred to M3", opts.BlobType)
	}
	if cfg.BlobStorage == domain.BlobStorageExternalRef {
		return "", errors.New("core.Put: BlobStorage: ExternalRef deferred to a later milestone")
	}
	if cfg.ManifestStorage != domain.ManifestStorageRemote && cfg.ManifestStorage != "" {
		return "", fmt.Errorf("core.Put: ManifestStorage %q deferred to M2.2",
			cfg.ManifestStorage)
	}

	// TODO(M5.x): Two-Pass dedup path. Per docs/2. Internals/02 §2.1.1,
	// when a.Payload implements io.ReadSeeker AND the driver does not
	// declare CapSlowRead, we should hash on a first pass, probe the
	// dedup index, and skip the staging write on a hit. M1.4 ships
	// with One-Pass only — correct, but writes-then-dedups even on
	// hits. Acceptable for the M1 perimeter; revisit when S3 driver
	// arrives in M5 (CapSlowRead becomes a real signal there).

	// --- Phase 1: hash payload, decide inline vs target ---
	//
	// We always need the content hash up front: it is the dedup
	// key, the BlobRef of fresh blobs, and a deterministic input
	// to the manifest. The placement decision (inline body vs.
	// separate blob file) depends on size, which is also produced
	// here.
	//
	// For InlineFallback we speculatively read up to
	// InlineBlobLimit + 1 bytes. If the read returns at most
	// InlineBlobLimit bytes, the payload fits inline; otherwise
	// we have already consumed the head and must drain the rest
	// to staging via MultiReader.

	hashAlgo := string(cfg.ContentHasher)
	hasher, err := s.hashes.NewHasher(hashAlgo)
	if err != nil {
		return "", fmt.Errorf("core.Put: hasher: %w", err)
	}

	useInlineFallback := cfg.BlobStorage == domain.BlobStorageInlineFallback &&
		cfg.InlineBlobLimit > 0
	inlineLimit := cfg.InlineBlobLimit

	var (
		contentHash  domain.ContentHash
		originalSize int64
		inlineBytes  []byte // non-nil iff this Put goes inline
		blobRef      domain.BlobRef
		blobAddr     domain.PhysicalAddress
	)

	if useInlineFallback {
		head, err := io.ReadAll(io.LimitReader(a.Payload, inlineLimit+1))
		if err != nil {
			return "", fmt.Errorf("core.Put: read payload head: %w", err)
		}
		if int64(len(head)) <= inlineLimit {
			// Fits inline. Hash the head, no staging file, no
			// dedup probe — inline blobs do not participate in
			// dedup (their bytes live inside the manifest).
			if _, err := hasher.Write(head); err != nil {
				return "", fmt.Errorf("core.Put: hash inline: %w", err)
			}
			contentHash = domain.ContentHash(s.hashes.Format(hashAlgo, hasher.Sum(nil)))
			originalSize = int64(len(head))
			inlineBytes = head
			blobRef = domain.BlobRef(contentHash)
			// blobAddr stays zero: no driver entry for inline.
		} else {
			// Overflowed inline. Stream head + remainder to a
			// staging file, hashing the full thing.
			stagingPath, err := s.makeStagingPath()
			if err != nil {
				return "", err
			}
			combined := io.MultiReader(bytes.NewReader(head), a.Payload)
			tee := io.TeeReader(combined, hasher)
			counter := &countingReader{r: tee}
			if err := s.drv.Put(ctx, stagingPath, counter); err != nil {
				return "", fmt.Errorf("core.Put: stage payload: %w", err)
			}
			contentHash = domain.ContentHash(s.hashes.Format(hashAlgo, hasher.Sum(nil)))
			originalSize = counter.n
			blobRef, blobAddr, err = s.commitBlob(ctx, cfg, stagingPath, contentHash, originalSize)
			if err != nil {
				return "", err
			}
		}
	} else {
		// Plain Target path: stream straight to staging.
		stagingPath, err := s.makeStagingPath()
		if err != nil {
			return "", err
		}
		tee := io.TeeReader(a.Payload, hasher)
		counter := &countingReader{r: tee}
		if err := s.drv.Put(ctx, stagingPath, counter); err != nil {
			return "", fmt.Errorf("core.Put: stage payload: %w", err)
		}
		contentHash = domain.ContentHash(s.hashes.Format(hashAlgo, hasher.Sum(nil)))
		originalSize = counter.n
		blobRef, blobAddr, err = s.commitBlob(ctx, cfg, stagingPath, contentHash, originalSize)
		if err != nil {
			return "", err
		}
	}

	// --- Phase 2: build manifest and compute its ArtifactID ---
	//
	// LayoutHeader.BlobStorage records HOW this particular blob
	// is laid out, regardless of the StoreConfig that was in
	// effect at write time. The read path inspects the header,
	// not the current config — that is what makes manifests
	// stable across config changes.
	//
	// Per docs §7.2: BlobRef is set on every manifest, including
	// inline ones (where it equals the ContentHash of the
	// embedded bytes).

	layout := "Target"
	if inlineBytes != nil {
		layout = "Inline"
	}
	createdAt := time.Now().UTC()
	manifest := domain.Manifest{
		Type:           domain.ManifestTypeBlob,
		Namespace:      opts.Namespace,
		SessionID:      opts.SessionID,
		CreatedAt:      createdAt,
		ContentHash:    contentHash,
		OriginalSize:   originalSize,
		BlobRef:        blobRef,
		LayoutHeader:   domain.LayoutHeader{BlobStorage: layout},
		Pipeline:       []domain.PipelineStage{},
		InlineBlob:     inlineBytes,
		RetentionUntil: opts.RetentionUntil,
		Metadata:       a.Metadata,
	}
	artifactID, manifestBytes, manifest, err := manifestcodec.ComputeArtifactID(
		manifest, hashAlgo, s.hashes,
		cfg.ManifestEncoding, cfg.ManifestCrypto,
	)
	if err != nil {
		// On encoding/crypto deferral the blob (if any) is already
		// committed. We do NOT roll it back: the orphan blob is
		// harmless (ref_count stays 0, GC reaps it). Rolling back
		// would require an inverse of Driver.Rename, which can
		// race against a parallel Put deduping on the same content.
		return "", fmt.Errorf("core.Put: compute artifact id: %w", err)
	}

	// --- Phase 3: write the manifest file ---

	manifestPath, err := blobpath.ManifestPath(artifactID)
	if err != nil {
		return "", fmt.Errorf("core.Put: manifest path: %w", err)
	}
	if err := s.drv.Put(ctx, manifestPath, bytes.NewReader(manifestBytes)); err != nil {
		return "", fmt.Errorf("core.Put: write manifest: %w", err)
	}

	// --- Phase 4: index ---
	//
	// For inline manifests blobAddr is the zero PhysicalAddress.
	// IndexManifest dispatches on manifest.LayoutHeader.BlobStorage
	// to skip the blobs-table insertion for inline; the manifest
	// itself is still indexed so Walk and GetBySession find it.

	if err := s.index.IndexManifest(manifest, blobAddr, nil, nil); err != nil {
		// Manifest file is on disk but unindexed. RebuildIndexAgent
		// (M3) is the recovery path. We surface the error so the
		// caller can retry the index step or reissue Put (which
		// will dedup the blob and re-attempt the manifest).
		return "", fmt.Errorf("core.Put: index manifest: %w", err)
	}

	// --- Phase 5: emit ---

	s.publish(EventManifestSaved, ManifestSavedPayload{
		Manifest:  manifest,
		IsTransit: false,
	})

	return artifactID, nil
}

// commitBlob is the tail of the Target write path: dedup probe,
// then either drop the staging file (hit) or rename it to its
// final hash-derived path (miss). Returns the BlobRef and the
// PhysicalAddress of where the blob actually lives now.
func (s *store) commitBlob(
	ctx context.Context,
	cfg domain.StoreConfig,
	stagingPath string,
	contentHash domain.ContentHash,
	originalSize int64,
) (domain.BlobRef, domain.PhysicalAddress, error) {
	existingRef, found, err := s.index.ExistsByContent(contentHash, originalSize)
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: dedup probe: %w", err)
	}
	if found {
		if err := s.drv.Remove(ctx, stagingPath); err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: drop staging: %w", err)
		}
		addr, err := s.index.Resolve(existingRef)
		if err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: resolve existing blob: %w", err)
		}
		return domain.BlobRef(existingRef), addr, nil
	}
	// Fresh blob. BlobRef equals ContentHash in M1.4 (no Pipeline
	// transforms the bytes between content and on-disk form).
	blobRef := domain.BlobRef(contentHash)
	finalPath, err := blobpath.Resolve(cfg.PathTopology, domain.BlobTypeRegular, string(blobRef))
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: resolve blob path: %w", err)
	}
	if err := s.drv.Rename(ctx, stagingPath, finalPath); err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: commit blob: %w", err)
	}
	return blobRef, domain.PhysicalAddress{
		Workspace: domain.WorkspaceLocation,
		Path:      finalPath,
	}, nil
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
	if s.maintenanceMode() == domain.MaintenanceModeReadOnly {
		return errs.ErrStoreReadOnly
	}
	return nil
}

// validatePutInputs covers the cheap, side-effect-free checks that
// must reject before any I/O. Order matches the priority of
// docs/2. Internals/01 §1.4.
func validatePutInputs(a domain.Artifact, opts domain.PutOptions) error {
	if a.Payload == nil && opts.ExternalURI == "" {
		return errors.New("core.Put: nil Payload and no ExternalURI")
	}
	if len(opts.Namespace) > 255 {
		return errs.ErrNamespaceTooLong
	}
	if strings.HasPrefix(opts.Namespace, "system.") || opts.Namespace == "*" {
		return errs.ErrReservedNamespace
	}
	if len(opts.SessionID) > 255 {
		return errs.ErrSessionIDTooLong
	}
	if len(a.Metadata) > 64*1024 {
		return errs.ErrMetadataTooLarge
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
	id, err := generateUUID()
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

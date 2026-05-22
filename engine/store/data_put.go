package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
	"scrinium.dev/engine/pipeline"
)

// Put records an artifact and returns its ArtifactID. It orchestrates
// four phases, each a helper below: materialize the blob (hash +
// placement), assemble the manifest (with its ArtifactID), persist the
// manifest file, and index it. A blob may go to a separate file
// (Target) or be embedded in the manifest (Inline); see materializeBlob.
func (s *store) Put(ctx context.Context, a domain.Artifact, opts domain.PutOptions) (domain.ArtifactID, error) {
	if err := s.enterWrite(ctx); err != nil {
		return "", err
	}
	if err := validatePutInputs(a, opts); err != nil {
		return "", err
	}

	cfg := s.snapshotConfig()
	if err := s.checkPutSupported(cfg, opts); err != nil {
		return "", err
	}

	blob, err := s.materializeBlob(ctx, cfg, a, opts)
	if err != nil {
		return "", err
	}

	manifest, manifestBytes, err := s.assembleManifest(cfg, a, opts, blob)
	if err != nil {
		return "", err
	}

	if err := s.persistManifest(ctx, manifest, manifestBytes, blob.addr); err != nil {
		return "", err
	}

	s.publish(event.EventManifestSaved, event.ManifestSavedPayload{Manifest: manifest})
	return manifest.ArtifactID, nil
}

// blobResult carries the outcome of materializeBlob: the addressing
// hashes, the original payload size, the pipeline stages recorded for
// the manifest, and either a non-nil inline body or a blob address.
type blobResult struct {
	contentHash  domain.ContentHash
	blobRef      domain.BlobRef
	originalSize int64
	stages       []domain.PipelineStage
	inlineBytes  []byte                 // non-nil iff the blob is inline
	addr         domain.PhysicalAddress // zero for inline
}

// materializeBlob hashes the payload and places it. For InlineFallback
// it speculatively reads up to InlineBlobLimit+1 bytes: at or under the
// limit the bytes are embedded in the manifest; over it, the consumed
// head is spliced back and the payload streams to a Target blob. Plain
// stores always stream to Target.
func (s *store) materializeBlob(ctx context.Context, cfg domain.StoreConfig, a domain.Artifact, opts domain.PutOptions) (blobResult, error) {
	hashAlgo := string(cfg.ContentHasher)
	// ADR-58: resolve the write KeyID once and thread it through both
	// the blob pipeline and the manifest body, so a blob and its
	// manifest encrypt under the same key.
	writeKeyID := s.resolveWriteKeyID(opts.Namespace)

	inlineFallback := cfg.BlobStorage == domain.BlobStorageInlineFallback && cfg.InlineBlobLimit > 0

	if inlineFallback {
		head, err := io.ReadAll(io.LimitReader(a.Payload, cfg.InlineBlobLimit+1))
		if err != nil {
			return blobResult{}, fmt.Errorf("store.Put: read payload head: %w", err)
		}
		if int64(len(head)) <= cfg.InlineBlobLimit {
			return s.materializeInline(hashAlgo, head)
		}
		// Overflowed inline: stream head+rest to Target.
		combined := io.MultiReader(bytes.NewReader(head), a.Payload)
		return s.streamToTarget(ctx, cfg, hashAlgo, writeKeyID, combined)
	}
	return s.streamToTarget(ctx, cfg, hashAlgo, writeKeyID, a.Payload)
}

// materializeInline hashes an already-buffered payload and returns it
// as an inline blob. No driver entry, no dedup probe; BlobRef equals
// the ContentHash of the embedded bytes (docs §7.2).
func (s *store) materializeInline(hashAlgo string, body []byte) (blobResult, error) {
	h, err := s.hashes.NewHasher(hashAlgo)
	if err != nil {
		return blobResult{}, fmt.Errorf("store.Put: hasher: %w", err)
	}
	if _, err := h.Write(body); err != nil {
		return blobResult{}, fmt.Errorf("store.Put: hash inline: %w", err)
	}
	ch := domain.ContentHash(s.hashes.Format(hashAlgo, h.Sum(nil)))
	return blobResult{
		contentHash:  ch,
		blobRef:      domain.BlobRef(ch),
		originalSize: int64(len(body)),
		stages:       []domain.PipelineStage{},
		inlineBytes:  body,
	}, nil
}

// streamToTarget runs the active pipeline over input, stages the
// result, then commits it to a blob slot (dedup hit drops the staging
// file; miss renames it into place).
func (s *store) streamToTarget(ctx context.Context, cfg domain.StoreConfig, hashAlgo, writeKeyID string, input io.Reader) (blobResult, error) {
	stagingPath := s.makeStagingPath()

	stream, pp, err := s.pipelineRunner().BuildPut(hashAlgo, input, cfg.Pipeline, pipeline.EncodeContext{
		KeyID:          writeKeyID,
		EncryptedDedup: cfg.EncryptedDedup, // ADR-58: IV mode for the crypto stage
		SegmentSize:    cfg.SegmentSize,    // ADR-59: segmented AEAD frame size
	})
	if err != nil {
		return blobResult{}, fmt.Errorf("store.Put: %w", err)
	}

	if err := s.drv.Put(ctx, stagingPath, stream); err != nil {
		return blobResult{}, fmt.Errorf("store.Put: stage payload: %w", err)
	}

	contentHash, blobRef, stages := pp.Finalize()
	originalSize := pp.ContentBytesRead() // measured on the pre-pipeline input

	commitRef, addr, err := s.commitBlob(ctx, cfg, stagingPath, contentHash,
		originalSize, blobRef, domain.CryptoIdentityOf(stages))
	if err != nil {
		return blobResult{}, err
	}
	return blobResult{
		contentHash:  contentHash,
		blobRef:      commitRef,
		originalSize: originalSize,
		stages:       stages,
		addr:         addr,
	}, nil
}

// assembleManifest builds the manifest from the blob result and
// computes its ArtifactID. LayoutHeader records how this blob is laid
// out, independent of the current config — the read path trusts the
// header, which is what keeps manifests stable across config changes.
// For an encrypting config the DEK is borrowed under the crypto lock
// for the ComputeArtifactID call and wiped immediately after.
func (s *store) assembleManifest(cfg domain.StoreConfig, a domain.Artifact, opts domain.PutOptions, blob blobResult) (domain.Manifest, []byte, error) {
	layout := domain.LayoutTarget
	if blob.inlineBytes != nil {
		layout = domain.LayoutInline
	}
	manifest := domain.Manifest{
		Type:           domain.ManifestTypeBlob,
		Namespace:      opts.Namespace,
		SessionID:      opts.SessionID,
		CreatedAt:      time.Now().UTC(),
		ContentHash:    blob.contentHash,
		OriginalSize:   blob.originalSize,
		BlobRef:        blob.blobRef,
		LayoutHeader:   domain.LayoutHeader{BlobStorage: layout},
		Pipeline:       blob.stages,
		InlineBlob:     blob.inlineBytes,
		RetentionUntil: opts.RetentionUntil,
		Ext:            a.Ext,
		Usr:            a.Usr,
	}

	hashAlgo := string(cfg.ContentHasher)
	var (
		signed domain.Manifest
		raw    []byte
	)
	err := s.withWriteDEK(cfg, opts.Namespace, func(dek []byte, keyID string) error {
		id, fileBytes, sm, cerr := manifestcodec.ComputeArtifactID(
			manifest, hashAlgo, s.hashes,
			cfg.ManifestEncoding, cfg.ManifestCrypto, dek, keyID,
		)
		if cerr != nil {
			return cerr
		}
		sm.ArtifactID = id
		signed = sm
		raw = fileBytes
		return nil
	})
	if err != nil {
		// A blob (if any) is already committed; we do not roll it
		// back. An orphan blob is harmless — ref_count stays 0 and GC
		// reaps it — whereas an inverse Rename could race a parallel
		// dedup on the same content.
		return domain.Manifest{}, nil, fmt.Errorf("store.Put: compute artifact id: %w", err)
	}
	return signed, raw, nil
}

// persistManifest writes the manifest file and indexes it. For inline
// manifests addr is the zero PhysicalAddress; IndexManifest skips the
// blobs-table insert but still indexes the manifest so Walk and
// GetBySession find it.
func (s *store) persistManifest(ctx context.Context, manifest domain.Manifest, manifestBytes []byte, addr domain.PhysicalAddress) error {
	manifestPath, err := blobpath.ManifestPath(manifest.ArtifactID)
	if err != nil {
		return fmt.Errorf("store.Put: manifest path: %w", err)
	}
	if err := s.drv.Put(ctx, manifestPath, bytes.NewReader(manifestBytes)); err != nil {
		return fmt.Errorf("store.Put: write manifest: %w", err)
	}
	if err := s.index.IndexManifest(ctx, manifest, addr, nil, nil); err != nil {
		// Manifest is on disk but unindexed; the rebuild agent is the
		// recovery path. Surface so the caller can retry.
		return fmt.Errorf("store.Put: index manifest: %w", err)
	}
	return nil
}

// withWriteDEK borrows a DEK copy for an encrypting write and
// guarantees it is wiped before returning. For a Plain config it calls
// fn with a nil DEK and empty keyID. The DEK never escapes fn, so no
// write path can leak it by forgetting to wipe.
func (s *store) withWriteDEK(cfg domain.StoreConfig, namespace string, fn func(dek []byte, keyID string) error) error {
	if cfg.ManifestCrypto == "" || cfg.ManifestCrypto == domain.ManifestCryptoPlain {
		return fn(nil, "")
	}
	dek, err := s.crypto.dekForWrite(cfg.ManifestCrypto)
	if err != nil {
		return err
	}
	defer aead.Wipe(dek)
	return fn(dek, s.resolveWriteKeyID(namespace))
}

// commitBlob is the tail of the Target write path: probe dedup, then
// drop the staging file (hit) or rename it into place (miss). Returns
// the BlobRef and the address where the blob now lives.
func (s *store) commitBlob(
	ctx context.Context,
	cfg domain.StoreConfig,
	stagingPath string,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
	crypto domain.CryptoIdentity,
) (domain.BlobRef, domain.PhysicalAddress, error) {
	existingRef, found, err := s.dedupProbe(ctx, contentHash, originalSize, blobRef, crypto)
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("store.Put: dedup probe: %w", err)
	}
	if found {
		if err := s.drv.Remove(ctx, stagingPath); err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("store.Put: drop staging: %w", err)
		}
		addr, err := s.index.Resolve(ctx, existingRef)
		if err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("store.Put: resolve existing blob: %w", err)
		}
		return domain.BlobRef(existingRef), addr, nil
	}
	finalPath, err := blobpath.Resolve(cfg.PathTopology, domain.BlobTypeRegular, string(blobRef))
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("store.Put: resolve blob path: %w", err)
	}
	if err := s.drv.Rename(ctx, stagingPath, finalPath); err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("store.Put: commit blob: %w", err)
	}
	return blobRef, domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: finalPath}, nil
}

// dedupProbe decides whether a staged blob duplicates an indexed one
// (ADR-58), branching on crypto-identity:
//
//   - Plain (empty identity): probe by (ContentHash, OriginalSize). A
//     keyless transform is reproducible, so a hit is a true byte
//     duplicate — share it (preserves dedup-independent-of-compression).
//   - Encrypted (non-empty identity): probe by BlobRef. Under random-IV
//     Disabled the staged BlobRef is unique, so this never hits and we
//     never dedup; under Convergent identical ciphertext hits, which is
//     the intended dedup. Probing by content here would risk sharing a
//     blob encrypted under a different IV.
func (s *store) dedupProbe(
	ctx context.Context,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
	crypto domain.CryptoIdentity,
) (string, bool, error) {
	if crypto == "" {
		return s.index.ExistsByContent(ctx, contentHash, originalSize, "")
	}
	if _, err := s.index.Resolve(ctx, string(blobRef)); err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(blobRef), true, nil
}

// resolveWriteKeyID asks the resolver which KeyID a new artifact in
// this namespace encrypts under (ADR-58). The resolver reference is
// snapshotted under the crypto lock but ResolveWriteKey runs without
// it — it must be a cheap, non-blocking lookup. Returns "" for an
// unencrypted store.
func (s *store) resolveWriteKeyID(namespace string) string {
	r := s.crypto.resolver()
	if r == nil {
		return ""
	}
	return r.ResolveWriteKey(pipeline.KeyContext{Namespace: namespace})
}

// checkPutSupported rejects configurations and options whose support
// is not yet wired, before any I/O. These are invariants of the
// active config plus the per-call BlobType; catching them here keeps
// the write helpers free of feature-gate branches.
func (s *store) checkPutSupported(cfg domain.StoreConfig, opts domain.PutOptions) error {
	if err := s.pipelineRunner().ValidateAlgos(cfg.Pipeline); err != nil {
		return fmt.Errorf("store.Put: %w", err)
	}
	if opts.BlobType != "" && opts.BlobType != domain.BlobTypeRegular {
		return fmt.Errorf("store.Put: BlobType %q not supported (TODO M3)", opts.BlobType)
	}
	if cfg.BlobStorage == domain.BlobStorageExternalRef {
		return errors.New("store.Put: BlobStorage: ExternalRef not yet supported")
	}
	if cfg.ManifestStorage != domain.ManifestStorageRemote && cfg.ManifestStorage != "" {
		// Local and Replicated need HostStorage as the transit buffer,
		// not yet wired (TODO M4.2); only Remote (the default) works.
		return fmt.Errorf("store.Put: ManifestStorage %q requires HostStorage (TODO M4.2)", cfg.ManifestStorage)
	}
	if cfg.BlobStorage == domain.BlobStorageInlineFallback && cfg.InlineBlobLimit > 0 && len(cfg.Pipeline) > 0 {
		// Inline + Pipeline is reserved (M2-extra); refuse early so a
		// user never gets untransformed bytes inside the manifest.
		return errPipelineWithInline
	}
	return nil
}

// validatePutInputs covers the cheap, side-effect-free checks that
// reject before any I/O.
func validatePutInputs(a domain.Artifact, opts domain.PutOptions) error {
	if a.Payload == nil && opts.ExternalURI == "" {
		return errors.New("store.Put: nil Payload and no ExternalURI")
	}
	if len(opts.Namespace) > domain.MaxNamespaceLen {
		return errs.ErrNamespaceTooLong
	}
	if strings.HasPrefix(opts.Namespace, domain.NamespaceSystemPrefix) || opts.Namespace == domain.NamespaceWildcard {
		return errs.ErrReservedNamespace
	}
	if len(opts.SessionID) > domain.MaxSessionIDLen {
		return errs.ErrSessionIDTooLong
	}
	if len(a.Ext) > domain.MaxExtSize {
		return errs.ErrExtTooLarge
	}
	if len(a.Usr) > domain.MaxUsrSize {
		return errs.ErrUsrTooLarge
	}
	return nil
}

// makeStagingPath returns a fresh unique path under the staging
// prefix; uniqueness comes from a UUIDv4.
func (s *store) makeStagingPath() string {
	return domain.StagingPrefix + "/" + uuid.NewString()
}

// PutBlob is a level-3 decorator entry point for chunker.Wrapper
// (M5.2) to write anonymous chunks without a manifest. It is deferred
// to M5 and is slated to move off DataStore onto a dedicated BlobStore
// interface at the start of M5; until then the stub keeps the contract
// honest rather than silently succeeding.
func (s *store) PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error) {
	return "", fmt.Errorf("%w: store.PutBlob is deferred to M5 (chunker.Wrapper); the method moves to BlobStore at M5 start",
		errs.ErrNotImplemented)
}

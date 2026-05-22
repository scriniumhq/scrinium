package blobwriter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
	"scrinium.dev/engine/pipeline"
)

// Writer is the artifact write-path engine bound to a store's Driver,
// StoreIndex, and registries. Construct once with New; the value is a
// thin handle over its dependencies and holds no mutable state.
type Writer struct {
	drv          driver.Driver
	index        coreapi.StoreIndex
	hashes       domain.HashRegistry
	transformers pipeline.TransformerRegistry
}

// New wires a Writer to its dependencies. Mirrors systemstore.New:
// the store layer owns these objects and injects them so blobwriter
// never reaches into *store internals.
func New(
	drv driver.Driver,
	index coreapi.StoreIndex,
	hashes domain.HashRegistry,
	transformers pipeline.TransformerRegistry,
) *Writer {
	return &Writer{drv: drv, index: index, hashes: hashes, transformers: transformers}
}

// Result carries the outcome of Materialize: the addressing hashes, the
// original payload size, the pipeline stages recorded for the manifest,
// and either a non-nil inline body or a committed blob address. Fields
// are exported because the value crosses back into the store package
// (Put threads it into AssembleManifest and reads Addr for
// PersistManifest).
type Result struct {
	ContentHash  domain.ContentHash
	BlobRef      domain.BlobRef
	OriginalSize int64
	Stages       []domain.PipelineStage
	InlineBytes  []byte                 // non-nil iff the blob is inline
	Addr         domain.PhysicalAddress // zero for inline
}

// runner returns a pipeline.Runner bound to this writer's registries. A
// Runner is a cheap wrapper, built per operation rather than held as a
// field.
func (w *Writer) runner() *pipeline.Runner {
	return pipeline.NewRunner(w.hashes, w.transformers)
}

// Materialize hashes the payload and places it. For InlineFallback it
// speculatively reads up to InlineBlobLimit+1 bytes: at or under the
// limit the bytes are embedded in the manifest; over it, the consumed
// head is spliced back and the payload streams to a Target blob. Plain
// stores always stream to Target.
//
// writeKeyID is resolved once by the caller (ADR-58) and threaded into
// both the blob pipeline here and the manifest body in AssembleManifest,
// so a blob and its manifest encrypt under the same key.
func (w *Writer) Materialize(ctx context.Context, cfg domain.StoreConfig, a domain.Artifact, opts domain.PutOptions, writeKeyID string) (Result, error) {
	hashAlgo := string(cfg.ContentHasher)
	inlineFallback := cfg.BlobStorage == domain.BlobStorageInlineFallback && cfg.InlineBlobLimit > 0

	if inlineFallback {
		head, err := io.ReadAll(io.LimitReader(a.Payload, cfg.InlineBlobLimit+1))
		if err != nil {
			return Result{}, fmt.Errorf("read payload head: %w", err)
		}
		if int64(len(head)) <= cfg.InlineBlobLimit {
			return w.materializeInline(hashAlgo, head)
		}
		// Overflowed inline: stream head+rest to Target.
		combined := io.MultiReader(bytes.NewReader(head), a.Payload)
		return w.streamToTarget(ctx, cfg, hashAlgo, writeKeyID, combined)
	}
	return w.streamToTarget(ctx, cfg, hashAlgo, writeKeyID, a.Payload)
}

// materializeInline hashes an already-buffered payload and returns it
// as an inline blob. No driver entry, no dedup probe; BlobRef equals
// the ContentHash of the embedded bytes (docs §7.2).
func (w *Writer) materializeInline(hashAlgo string, body []byte) (Result, error) {
	h, err := w.hashes.NewHasher(hashAlgo)
	if err != nil {
		return Result{}, fmt.Errorf("hasher: %w", err)
	}
	if _, err := h.Write(body); err != nil {
		return Result{}, fmt.Errorf("hash inline: %w", err)
	}
	ch := domain.ContentHash(w.hashes.Format(hashAlgo, h.Sum(nil)))
	return Result{
		ContentHash:  ch,
		BlobRef:      domain.BlobRef(ch),
		OriginalSize: int64(len(body)),
		Stages:       []domain.PipelineStage{},
		InlineBytes:  body,
	}, nil
}

// streamToTarget runs the active pipeline over input, stages the
// result, then commits it to a blob slot (dedup hit drops the staging
// file; miss renames it into place).
func (w *Writer) streamToTarget(ctx context.Context, cfg domain.StoreConfig, hashAlgo, writeKeyID string, input io.Reader) (Result, error) {
	stagingPath := makeStagingPath()

	stream, pp, err := w.runner().BuildPut(hashAlgo, input, cfg.Pipeline, pipeline.EncodeContext{
		KeyID:          writeKeyID,
		EncryptedDedup: cfg.EncryptedDedup, // ADR-58: IV mode for the crypto stage
		SegmentSize:    cfg.SegmentSize,    // ADR-59: segmented AEAD frame size
	})
	if err != nil {
		return Result{}, fmt.Errorf("build put pipeline: %w", err)
	}

	if err := w.drv.Put(ctx, stagingPath, stream); err != nil {
		return Result{}, fmt.Errorf("stage payload: %w", err)
	}

	contentHash, blobRef, stages := pp.Finalize()
	originalSize := pp.ContentBytesRead() // measured on the pre-pipeline input

	commitRef, addr, err := w.commitBlob(ctx, cfg, stagingPath, contentHash,
		originalSize, blobRef, domain.CryptoIdentityOf(stages))
	if err != nil {
		return Result{}, err
	}
	return Result{
		ContentHash:  contentHash,
		BlobRef:      commitRef,
		OriginalSize: originalSize,
		Stages:       stages,
		Addr:         addr,
	}, nil
}

// commitBlob is the tail of the Target write path: probe dedup, then
// drop the staging file (hit) or rename it into place (miss). Returns
// the BlobRef and the address where the blob now lives.
func (w *Writer) commitBlob(
	ctx context.Context,
	cfg domain.StoreConfig,
	stagingPath string,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
	crypto domain.CryptoIdentity,
) (domain.BlobRef, domain.PhysicalAddress, error) {
	existingRef, found, err := w.dedupProbe(ctx, contentHash, originalSize, blobRef, crypto)
	if err != nil {
		_ = w.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("dedup probe: %w", err)
	}
	if found {
		if err := w.drv.Remove(ctx, stagingPath); err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("drop staging: %w", err)
		}
		addr, err := w.index.Resolve(ctx, existingRef)
		if err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("resolve existing blob: %w", err)
		}
		return domain.BlobRef(existingRef), addr, nil
	}
	finalPath, err := blobpath.Resolve(cfg.PathTopology, domain.BlobTypeRegular, string(blobRef))
	if err != nil {
		_ = w.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("resolve blob path: %w", err)
	}
	if err := w.drv.Rename(ctx, stagingPath, finalPath); err != nil {
		_ = w.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("commit blob: %w", err)
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
func (w *Writer) dedupProbe(
	ctx context.Context,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
	crypto domain.CryptoIdentity,
) (string, bool, error) {
	if crypto == "" {
		return w.index.ExistsByContent(ctx, contentHash, originalSize, "")
	}
	if _, err := w.index.Resolve(ctx, string(blobRef)); err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(blobRef), true, nil
}

// AssembleManifest builds the manifest from the blob result and computes
// its ArtifactID. LayoutHeader records how this blob is laid out,
// independent of the current config — the read path trusts the header,
// which is what keeps manifests stable across config changes.
//
// dek and keyID come from the caller: for a Plain config dek is nil and
// keyID is empty; for an encrypting config the caller borrows the DEK
// under the crypto lock and wipes it after this returns. The DEK is used
// only for ComputeArtifactID and is never retained.
func (w *Writer) AssembleManifest(cfg domain.StoreConfig, a domain.Artifact, opts domain.PutOptions, blob Result, dek []byte, keyID string) (domain.Manifest, []byte, error) {
	layout := domain.LayoutTarget
	if blob.InlineBytes != nil {
		layout = domain.LayoutInline
	}
	manifest := domain.Manifest{
		Type:           domain.ManifestTypeBlob,
		Namespace:      opts.Namespace,
		SessionID:      opts.SessionID,
		CreatedAt:      time.Now().UTC(),
		ContentHash:    blob.ContentHash,
		OriginalSize:   blob.OriginalSize,
		BlobRef:        blob.BlobRef,
		LayoutHeader:   domain.LayoutHeader{BlobStorage: layout},
		Pipeline:       blob.Stages,
		InlineBlob:     blob.InlineBytes,
		RetentionUntil: opts.RetentionUntil,
		Ext:            a.Ext,
		Usr:            a.Usr,
	}

	hashAlgo := string(cfg.ContentHasher)
	id, fileBytes, sm, err := manifestcodec.ComputeArtifactID(
		manifest, hashAlgo, w.hashes,
		cfg.ManifestEncoding, cfg.ManifestCrypto, dek, keyID,
	)
	if err != nil {
		// A blob (if any) is already committed; we do not roll it
		// back. An orphan blob is harmless — ref_count stays 0 and GC
		// reaps it — whereas an inverse Rename could race a parallel
		// dedup on the same content.
		return domain.Manifest{}, nil, fmt.Errorf("compute artifact id: %w", err)
	}
	sm.ArtifactID = id
	return sm, fileBytes, nil
}

// PersistManifest writes the manifest file and indexes it. For inline
// manifests addr is the zero PhysicalAddress; IndexManifest skips the
// blobs-table insert but still indexes the manifest so Walk and
// GetBySession find it.
func (w *Writer) PersistManifest(ctx context.Context, manifest domain.Manifest, manifestBytes []byte, addr domain.PhysicalAddress) error {
	manifestPath, err := blobpath.ManifestPath(manifest.ArtifactID)
	if err != nil {
		return fmt.Errorf("manifest path: %w", err)
	}
	if err := w.drv.Put(ctx, manifestPath, bytes.NewReader(manifestBytes)); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := w.index.IndexManifest(ctx, manifest, addr, nil, nil); err != nil {
		// Manifest is on disk but unindexed; the rebuild agent is the
		// recovery path. Surface so the caller can retry.
		return fmt.Errorf("index manifest: %w", err)
	}
	return nil
}

// makeStagingPath returns a fresh unique path under the staging prefix;
// uniqueness comes from a UUIDv4.
func makeStagingPath() string {
	return domain.StagingPrefix + "/" + uuid.NewString()
}

package artifactio

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/errs"
)

// IO is the artifact I/O engine bound to a store's Driver, StoreIndex,
// and registries. Construct once with New; the value is a thin
// handle over its dependencies and holds no mutable state.
type IO struct {
	drv          driver.Driver
	index        index.StoreIndex
	hashes       domain.HashRegistry
	transformers pipeline.TransformerRegistry
}

// New wires an IO to its dependencies. The store layer owns these
// objects and injects them so artifactio never reaches into *store
// internals.
func New(
	drv driver.Driver,
	index index.StoreIndex,
	hashes domain.HashRegistry,
	transformers pipeline.TransformerRegistry,
) *IO {
	return &IO{drv: drv, index: index, hashes: hashes, transformers: transformers}
}

// Result carries the outcome of Materialize: the addressing hashes, the
// original payload size, the pipeline stages recorded for the manifest,
// and either a non-nil inline body or a committed blob address. Fields are
// exported because the value crosses back into the store package (Put
// threads it into AssembleManifest and reads Addr for PersistManifest).
type Result struct {
	ContentHash  domain.ContentHash
	BlobRef      domain.BlobRef
	OriginalSize int64
	Stages       []domain.PipelineStage
	InlineBytes  []byte                 // non-nil iff the blob is inline
	Addr         domain.PhysicalAddress // zero for inline
}

// runner returns a pipeline.Runner bound to this writer's registries. A
// Runner is a cheap wrapper, built per operation rather than held.
func (x *IO) runner() *pipeline.Runner {
	return pipeline.NewRunner(x.hashes, x.transformers)
}

// Materialize hashes the payload and places it. For Inline mode it
// speculatively reads up to InlineBlobLimit+1 bytes: at or under the limit
// the bytes are embedded in the manifest; over it, the consumed head is
// spliced back and the payload streams to a Target blob. Plain stores
// always stream to Target.
//
// writeKeyID is resolved once by the caller (ADR-58) and threaded into
// both the blob pipeline here and the manifest body in AssembleManifest,
// so a blob and its manifest encrypt under the same key.
func (x *IO) Materialize(ctx context.Context, cfg domain.StoreConfig, a domain.Artifact, opts domain.PutOptions, writeKeyID string) (Result, error) {
	hashAlgo := string(cfg.ContentHasher)
	inlineMode := cfg.BlobStorage == domain.BlobStorageInline && cfg.InlineBlobLimit > 0

	if inlineMode {
		head, err := io.ReadAll(io.LimitReader(a.Payload, cfg.InlineBlobLimit+1))
		if err != nil {
			return Result{}, fmt.Errorf("artifactio: read payload head: %w", err)
		}
		if int64(len(head)) <= cfg.InlineBlobLimit {
			return x.materializeInline(hashAlgo, head)
		}
		combined := io.MultiReader(bytes.NewReader(head), a.Payload)
		return x.streamToTarget(ctx, cfg, hashAlgo, writeKeyID, combined)
	}
	return x.streamToTarget(ctx, cfg, hashAlgo, writeKeyID, a.Payload)
}

// materializeInline hashes an already-buffered payload and returns it as
// an inline blob. No driver entry, no dedup probe; BlobRef equals the
// ContentHash of the embedded bytes (docs §7.2).
func (x *IO) materializeInline(hashAlgo string, body []byte) (Result, error) {
	h, err := x.hashes.NewHasher(hashAlgo)
	if err != nil {
		return Result{}, fmt.Errorf("artifactio: hasher: %w", err)
	}
	if _, err := h.Write(body); err != nil {
		return Result{}, fmt.Errorf("artifactio: hash inline: %w", err)
	}
	ch := domain.ContentHash(x.hashes.Format(hashAlgo, h.Sum(nil)))
	return Result{
		ContentHash:  ch,
		BlobRef:      domain.BlobRef(ch),
		OriginalSize: int64(len(body)),
		Stages:       []domain.PipelineStage{},
		InlineBytes:  body,
	}, nil
}

// streamToTarget runs the active pipeline over input, stages the result,
// then commits it to a blob slot (dedup hit drops the staging file; miss
// renames it into place).
func (x *IO) streamToTarget(ctx context.Context, cfg domain.StoreConfig, hashAlgo, writeKeyID string, input io.Reader) (Result, error) {
	stagingPath := makeStagingPath()

	stream, pp, err := x.runner().BuildPut(hashAlgo, input, cfg.Pipeline, pipeline.EncodeContext{
		KeyID:          writeKeyID,
		EncryptedDedup: cfg.EncryptedDedup, // ADR-58: IV mode for the crypto stage
		SegmentSize:    cfg.SegmentSize,    // ADR-59: segmented AEAD frame size
	})
	if err != nil {
		return Result{}, fmt.Errorf("artifactio: build put pipeline: %w", err)
	}

	if err := x.drv.Put(ctx, stagingPath, stream); err != nil {
		return Result{}, fmt.Errorf("artifactio: stage payload: %w", err)
	}

	contentHash, blobRef, stages := pp.Finalize()
	originalSize := pp.ContentBytesRead() // measured on the pre-pipeline input

	commitRef, addr, err := x.commitBlob(ctx, cfg, stagingPath, contentHash,
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

// commitBlob is the tail of the Target write path: probe dedup, then drop
// the staging file (hit) or rename it into place (miss). Returns the
// BlobRef and the address where the blob now lives.
func (x *IO) commitBlob(
	ctx context.Context,
	cfg domain.StoreConfig,
	stagingPath string,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
	crypto domain.CryptoIdentity,
) (domain.BlobRef, domain.PhysicalAddress, error) {
	existingRef, found, err := x.dedupProbe(ctx, contentHash, originalSize, blobRef, crypto)
	if err != nil {
		_ = x.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("artifactio: dedup probe: %w", err)
	}
	if found {
		if err := x.drv.Remove(ctx, stagingPath); err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("artifactio: drop staging: %w", err)
		}
		addr, err := x.index.Resolve(ctx, existingRef)
		if err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("artifactio: resolve existing blob: %w", err)
		}
		return domain.BlobRef(existingRef), addr, nil
	}
	finalPath, err := artifact.BlobPath(cfg.PathTopology, domain.BlobTypeRegular, string(blobRef))
	if err != nil {
		_ = x.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("artifactio: resolve blob path: %w", err)
	}
	if err := x.drv.Rename(ctx, stagingPath, finalPath); err != nil {
		_ = x.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("artifactio: commit blob: %w", err)
	}
	return blobRef, domain.PhysicalAddress{Path: finalPath}, nil
}

// dedupProbe decides whether a staged blob duplicates an indexed one
// (ADR-58), branching on crypto-identity:
//
//   - Plain (empty identity): probe by (ContentHash, OriginalSize). A
//     keyless transform is reproducible, so a hit is a true byte duplicate
//     — share it (preserves dedup-independent-of-compression).
//   - Encrypted (non-empty identity): probe by BlobRef. Under random-IV
//     Disabled the staged BlobRef is unique, so this never hits and we
//     never dedup; under Convergent identical ciphertext hits, the
//     intended dedup. Probing by content here would risk sharing a blob
//     encrypted under a different IV.
func (x *IO) dedupProbe(
	ctx context.Context,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
	crypto domain.CryptoIdentity,
) (string, bool, error) {
	if crypto == "" {
		return x.index.ExistsByContent(ctx, contentHash, originalSize, "")
	}
	if _, err := x.index.Resolve(ctx, string(blobRef)); err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(blobRef), true, nil
}

// AssembleManifest builds the manifest from the blob result, computes its
// floating ArtifactID (handle) and then its ManifestDigest. LayoutHeader
// records how this blob is laid out, independent of the current config —
// the read path trusts the header, which keeps manifests stable across
// config changes.
//
// Identity (ADR-73): in IdentityMode=Unique a fresh 16-byte nonce is mixed
// into the handle (every Put is distinct); in Coalesced the handle is
// deterministic (no nonce). The naming key is the public domain constant
// (Plain/Sealed v1). ComputeHandle must run before ComputeManifestDigest,
// because the handle is part of the body the digest hashes.
//
// dek and keyID come from the caller: for a Plain config dek is nil and
// keyID is empty; for an encrypting config the caller borrows the DEK
// under the crypto lock and wipes it after this returns. The DEK is used
// only for ComputeManifestDigest and is never retained.
func (x *IO) AssembleManifest(cfg domain.StoreConfig, a domain.Artifact, opts domain.PutOptions, blob Result, dek []byte, keyID string) (domain.Manifest, []byte, error) {
	layout := domain.LayoutTarget
	if blob.InlineBytes != nil {
		layout = domain.LayoutInline
	}
	manifest := domain.Manifest{
		Namespace:      opts.Namespace,
		SessionID:      opts.SessionID,
		CreatedAt:      time.Now().UTC(),
		ContentHash:    blob.ContentHash,
		OriginalSize:   blob.OriginalSize,
		LayoutHeader:   domain.LayoutHeader{BlobStorage: layout},
		Pipeline:       blob.Stages,
		InlineBlob:     blob.InlineBytes,
		RetentionUntil: opts.RetentionUntil,
		Ext:            a.Ext,
		Usr:            a.Usr,
	}
	if blob.BlobRef != "" {
		manifest.BlobRefs = []domain.BlobRef{blob.BlobRef}
	}

	hashAlgo := string(cfg.ContentHasher)

	nonce, err := identityNonce(cfg.IdentityMode)
	if err != nil {
		return domain.Manifest{}, nil, fmt.Errorf("artifactio: identity nonce: %w", err)
	}
	withHandle, err := artifact.ComputeHandle(manifest, hashAlgo, x.hashes, hashing.NamingKeyPublic, nonce)
	if err != nil {
		return domain.Manifest{}, nil, fmt.Errorf("artifactio: compute handle: %w", err)
	}

	_, fileBytes, sm, err := artifact.ComputeManifestDigest(
		withHandle, hashAlgo, x.hashes,
		cfg.ManifestEncoding, cfg.ManifestCrypto, dek, keyID,
	)
	if err != nil {
		// A blob (if any) is already committed; we do not roll it back. An
		// orphan blob is harmless — ref_count stays 0 and GC reaps it —
		// whereas an inverse Rename could race a parallel dedup on the same
		// content.
		return domain.Manifest{}, nil, fmt.Errorf("artifactio: compute manifest digest: %w", err)
	}
	return sm, fileBytes, nil
}

// identityNonce returns a fresh 16-byte nonce for IdentityMode=Unique (the
// default when unset) and nil for Coalesced, so the handle is unique
// per-Put or deterministic respectively (ADR-73).
func identityNonce(mode domain.IdentityMode) ([]byte, error) {
	if mode == domain.IdentityModeCoalesced {
		return nil, nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

// PersistManifest writes the manifest file and indexes it. The manifest
// file is named by its ManifestDigest (the physical form); the index row
// records both the digest and the floating ArtifactID so Load can resolve
// handle → digest. For inline manifests addr is the zero PhysicalAddress;
// IndexManifest skips the blobs-table insert but still indexes the
// manifest so Walk and GetBySession find it.
func (x *IO) PersistManifest(ctx context.Context, manifest domain.Manifest, manifestBytes []byte, addr domain.PhysicalAddress) error {
	manifestPath, err := artifact.ManifestPath(manifest.Digest)
	if err != nil {
		return fmt.Errorf("artifactio: manifest path: %w", err)
	}
	if err := x.drv.Put(ctx, manifestPath, bytes.NewReader(manifestBytes)); err != nil {
		return fmt.Errorf("artifactio: write manifest: %w", err)
	}
	if err := x.index.IndexManifest(ctx, manifest, addr); err != nil {
		// Manifest is on disk but unindexed; the rebuild agent is the
		// recovery path. Surface so the caller can retry.
		return fmt.Errorf("artifactio: index manifest: %w", err)
	}
	return nil
}

// makeStagingPath returns a fresh unique path under the staging prefix;
// uniqueness comes from a UUIDv4.
func makeStagingPath() string {
	return domain.StagingPrefix + "/" + uuid.NewString()
}

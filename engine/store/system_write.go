package store

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
)

// writeInlineSystemArtifact builds an Inline blob manifest in the
// given reserved namespace, encodes it, computes its ArtifactID,
// persists the manifest file through the driver, and indexes it.
//
// Used by callers that bypass Store.Put's namespace check: InitStore
// and the maintenance agents.
//
// Inline-only: the payload becomes manifest.InlineBlob — no staging,
// no separate blob file, no dedup, no Pipeline. System artifacts
// are short, unique per write, and never benefit from those.
func writeInlineSystemArtifact(
	ctx context.Context,
	drv driver.Driver,
	idx index.StoreIndex,
	hashes domain.HashRegistry,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
) (domain.ManifestDigest, error) {
	// ContentHash == BlobRef: the Pipeline is empty for system artifacts.
	contentHasher, err := hashes.NewHasher(hashAlgo)
	if err != nil {
		return "", fmt.Errorf("system artifact: content hasher: %w", err)
	}
	if _, err := contentHasher.Write(payload); err != nil {
		return "", fmt.Errorf("system artifact: hash payload: %w", err)
	}
	contentHash := domain.ContentHash(hashes.Format(hashAlgo, contentHasher.Sum(nil)))

	manifest := domain.Manifest{
		Namespace:    namespace,
		SessionID:    sessionID,
		ContentHash:  contentHash,
		BlobRefs:     []domain.BlobRef{domain.BlobRef(contentHash)},
		OriginalSize: int64(len(payload)),
		InlineBlob:   payload,
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutInline},
		CreatedAt:    time.Now().UTC(),
	}

	digest, fileBytes, manifest, err := artifact.ComputeManifestDigest(
		manifest, hashAlgo, hashes,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain,
		nil, "")
	if err != nil {
		return "", fmt.Errorf("system artifact: compute id: %w", err)
	}

	manifestPath, err := artifact.ManifestPath(digest)
	if err != nil {
		return "", fmt.Errorf("system artifact: path: %w", err)
	}
	if err := drv.Put(ctx, manifestPath, bytes.NewReader(fileBytes)); err != nil {
		return "", fmt.Errorf("system artifact: put manifest: %w", err)
	}

	if err := idx.IndexManifest(ctx, manifest, domain.PhysicalAddress{
		Path: manifestPath,
	}); err != nil {
		return "", fmt.Errorf("system artifact: index: %w", err)
	}

	return digest, nil
}

// writeInlineSystemArtifactUnindexed is writeInlineSystemArtifact without
// the IndexManifest step (see WithoutIndex).
func writeInlineSystemArtifactUnindexed(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
) (domain.ManifestDigest, error) {
	contentHasher, err := hashes.NewHasher(hashAlgo)
	if err != nil {
		return "", fmt.Errorf("system artifact (no-index): content hasher: %w", err)
	}
	if _, err := contentHasher.Write(payload); err != nil {
		return "", fmt.Errorf("system artifact (no-index): hash payload: %w", err)
	}
	contentHash := domain.ContentHash(hashes.Format(hashAlgo, contentHasher.Sum(nil)))

	manifest := domain.Manifest{
		Namespace:    namespace,
		SessionID:    sessionID,
		ContentHash:  contentHash,
		BlobRefs:     []domain.BlobRef{domain.BlobRef(contentHash)},
		OriginalSize: int64(len(payload)),
		InlineBlob:   payload,
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutInline},
		CreatedAt:    time.Now().UTC(),
	}

	digest, fileBytes, _, err := artifact.ComputeManifestDigest(
		manifest, hashAlgo, hashes,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain,
		nil, "")
	if err != nil {
		return "", fmt.Errorf("system artifact (no-index): compute id: %w", err)
	}

	manifestPath, err := artifact.ManifestPath(digest)
	if err != nil {
		return "", fmt.Errorf("system artifact (no-index): path: %w", err)
	}
	if err := drv.Put(ctx, manifestPath, bytes.NewReader(fileBytes)); err != nil {
		return "", fmt.Errorf("system artifact (no-index): put manifest: %w", err)
	}

	return digest, nil
}

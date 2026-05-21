package store

// system_artifact_unindexed.go — writeInlineSystemArtifact's
// twin that skips the StoreIndex.IndexManifest step. Used by
// SystemStore.Put when called with WithoutIndex() — most notably
// for StoreIndex snapshots, which cannot live in the index they
// are snapshotting.
//
// The rest of the flow (hash payload, build inline manifest,
// compute ArtifactID, write manifest file) is identical to
// writeInlineSystemArtifact; only step 5 (Index) is omitted.

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
)

func writeInlineSystemArtifactUnindexed(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
) (domain.ArtifactID, error) {
	contentHasher, err := hashes.NewHasher(hashAlgo)
	if err != nil {
		return "", fmt.Errorf("system artifact (no-index): content hasher: %w", err)
	}
	if _, err := contentHasher.Write(payload); err != nil {
		return "", fmt.Errorf("system artifact (no-index): hash payload: %w", err)
	}
	contentHash := domain.ContentHash(hashes.Format(hashAlgo, contentHasher.Sum(nil)))

	manifest := domain.Manifest{
		Type:         domain.ManifestTypeBlob,
		Namespace:    namespace,
		SessionID:    sessionID,
		ContentHash:  contentHash,
		BlobRef:      domain.BlobRef(contentHash),
		OriginalSize: int64(len(payload)),
		InlineBlob:   payload,
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutInline},
		CreatedAt:    time.Now().UTC(),
	}

	id, fileBytes, _, err := manifestcodec.ComputeArtifactID(
		manifest, hashAlgo, hashes,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain,
		nil, "")
	if err != nil {
		return "", fmt.Errorf("system artifact (no-index): compute id: %w", err)
	}

	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return "", fmt.Errorf("system artifact (no-index): path: %w", err)
	}
	if err := drv.Put(ctx, manifestPath, bytes.NewReader(fileBytes)); err != nil {
		return "", fmt.Errorf("system artifact (no-index): put manifest: %w", err)
	}

	return id, nil
}

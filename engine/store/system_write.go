package store

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
)

// writeInlineSystemArtifact builds an Inline blob manifest in the
// given reserved namespace, encodes it, computes its ArtifactID,
// persists the manifest file through the driver, and indexes it.
//
// Used by callers that bypass Store.Put's namespace check:
// InitStore (system.config) and, in M3+, the Scrub/Snapshot/
// Maintenance agents (system.state).
//
// Inline-only: the payload becomes manifest.InlineBlob — no staging,
// no separate blob file, no dedup, no Pipeline. System artifacts
// are short, unique per write, and never benefit from those.
func writeInlineSystemArtifact(
	ctx context.Context,
	drv driver.Driver,
	idx coreapi.StoreIndex,
	hashes domain.HashRegistry,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
) (domain.ArtifactID, error) {
	// 1. Hash the payload — used as ContentHash and BlobRef
	//    (M1.4: equal because Pipeline is empty).
	contentHasher, err := hashes.NewHasher(hashAlgo)
	if err != nil {
		return "", fmt.Errorf("system artifact: content hasher: %w", err)
	}
	if _, err := contentHasher.Write(payload); err != nil {
		return "", fmt.Errorf("system artifact: hash payload: %w", err)
	}
	contentHash := domain.ContentHash(hashes.Format(hashAlgo, contentHasher.Sum(nil)))

	// 2. Build the manifest.
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

	// 3. Encode and hash to get the ArtifactID. ComputeArtifactID
	//    folds the encode+hash+assign cycle into one call.
	id, fileBytes, manifest, err := manifestcodec.ComputeArtifactID(
		manifest, hashAlgo, hashes,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain,
		nil, "")
	if err != nil {
		return "", fmt.Errorf("system artifact: compute id: %w", err)
	}

	// 4. Persist the manifest file. drv.Put is atomic.
	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return "", fmt.Errorf("system artifact: path: %w", err)
	}
	if err := drv.Put(ctx, manifestPath, bytes.NewReader(fileBytes)); err != nil {
		return "", fmt.Errorf("system artifact: put manifest: %w", err)
	}

	// 5. Index. WalkSystem(namespace) reads from here.
	if err := idx.IndexManifest(ctx, manifest, domain.PhysicalAddress{
		Workspace: domain.WorkspaceLocation,
		Path:      manifestPath,
	}, nil, nil); err != nil {
		return "", fmt.Errorf("system artifact: index: %w", err)
	}

	return id, nil
}

// writeInlineSystemArtifactUnindexed — writeInlineSystemArtifact's
// twin that skips the StoreIndex.IndexManifest step. Used by
// SystemStore.Put when called with WithoutIndex() — most notably
// for StoreIndex snapshots, which cannot live in the index they
// are snapshotting.
//
// The rest of the flow (hash payload, build inline manifest,
// compute ArtifactID, write manifest file) is identical to
// writeInlineSystemArtifact; only step 5 (Index) is omitted.
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

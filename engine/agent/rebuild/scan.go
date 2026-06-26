package rebuild

import (
	"context"
	"fmt"
	"io"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// scanManifests walks manifest files on the Location and reindexes them.
// since filters by modification time: the zero time scans everything (full
// rebuild); a non-zero time scans only the tail (checkpoint fast-path).
// Manifest paths are collected first (a streaming List whose callback only
// appends), then each file is fetched, decoded, and indexed — the per-file
// index writes must not run inside the List cursor.
func (a *rebuildAgent) scanManifests(ctx context.Context, keys domain.KeyProvider, since time.Time) error {
	var paths []string
	listErr := a.drv.ListObjectsWithModTime(ctx, manifestsPrefix, since,
		func(om driver.ObjectMeta) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			paths = append(paths, om.Path)
			return nil
		})
	if listErr != nil && !agent.IsCtxErr(listErr) {
		return fmt.Errorf("list manifests: %w", listErr)
	}

	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.reindexManifestFile(ctx, p, keys); err != nil {
			if agent.IsCtxErr(err) {
				return err
			}
			// A single unreadable/unsupported manifest must not abort the
			// whole rebuild; it is recorded via a progress event and the
			// scan continues. Encrypted manifests land here only when no
			// KeyProvider is available (an unencrypted Store, or a Store
			// whose key material could not be resolved).
			a.bus.Publish(event.Event{Type: event.EventAgentProgress, Payload: event.AgentProgressPayload{
				AgentType: "rebuild", StoreID: a.storeID,
			}})
			continue
		}
	}
	return nil
}

// reindexManifestFile fetches one manifest file, decodes it, and writes
// the reconstructed index rows.
func (a *rebuildAgent) reindexManifestFile(ctx context.Context, path string, keys domain.KeyProvider) error {
	digest, err := artifact.DigestFromManifestPath(path)
	if err != nil {
		return fmt.Errorf("parse manifest id from %q: %w", path, err)
	}
	rc, err := a.drv.Get(ctx, path)
	if err != nil {
		return fmt.Errorf("get manifest %q: %w", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(rc, domain.MaxManifestSize+1))
	if cerr := rc.Close(); cerr != nil {
		a.Logger().Debug("rebuild: manifest reader close failed", "path", path, "err", cerr)
	}
	if err != nil {
		return fmt.Errorf("read manifest %q: %w", path, err)
	}
	if len(data) > domain.MaxManifestSize {
		return fmt.Errorf("manifest %q too large: %w", path, errs.ErrManifestTooLarge)
	}

	var m domain.Manifest
	if keys != nil {
		// A KeyProvider is wired: DecodeEncrypted forwards Plain files and
		// decrypts encrypted ones, so both kinds are reconstructed rather
		// than the encrypted ones being skipped.
		m, err = artifact.DecodeEncrypted(data, keys)
	} else {
		// No key material: Plain only. An encrypted manifest returns
		// errs.ErrUnsupportedCrypto and is skipped by scanManifests.
		m, err = artifact.Decode(data)
	}
	if err != nil {
		return fmt.Errorf("decode manifest %q: %w", path, err)
	}
	a.countScanned()
	// The handle (m.ArtifactID) is serialised in the body and set by
	// Decode; the digest is the file name. A handle-less (system)
	// artifact falls back to its digest as ArtifactID.
	m.Digest = digest

	// Headless pack containers (empty slot) and chunked composites carry
	// chunk/packed-entry data absent from domain.Manifest on M3 — there is
	// nothing to reconstruct yet, so skip them rather than fake an index
	// row. Detect on the raw decoded manifest, before the handle backfill
	// below would mask an empty slot (ADR-83/92).
	if m.IsContainer() || m.IsComposite() {
		return nil
	}

	// User artifact: the handle (m.ArtifactID) is serialised in the body
	// and set by Decode. Fall back to the digest defensively if a
	// handle-bearing manifest somehow lacks one.
	if m.ArtifactID == "" {
		m.ArtifactID = domain.ArtifactID(digest)
	}
	return a.indexBlob(ctx, m)
}

// indexBlob reconstructs the IndexManifest arguments for a Blob manifest.
// Inline manifests carry their bytes in the body and have no blobs row;
// Target manifests resolve to a standalone blob file whose path is
// derived from the topology and the BlobRef.
func (a *rebuildAgent) indexBlob(ctx context.Context, m domain.Manifest) error {
	var addr domain.PhysicalAddress
	if m.LayoutHeader.BlobStorage == domain.LayoutTarget {
		topology := a.store.Config().PathTopology
		p, err := artifact.BlobPath(topology, domain.BlobTypeRegular, string(m.BlobRefs[0]))
		if err != nil {
			return fmt.Errorf("blob path for %q: %w", m.BlobRefs[0], err)
		}
		addr = domain.PhysicalAddress{Path: p}
	}
	// Blob manifests reference no chunks and no packed entries.
	if err := a.idx.IndexManifest(ctx, m, addr); err != nil {
		return fmt.Errorf("index manifest %q: %w", m.ArtifactID, err)
	}
	a.countIndexed(m.LayoutHeader.BlobStorage == domain.LayoutTarget)
	return nil
}

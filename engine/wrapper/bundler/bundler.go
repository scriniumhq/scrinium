package bundler

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index/extension"
)

// indexExtension is the bundler's index-side half (ADR-84/86): it
// OWNS the placement map of packed artifacts — the table that used
// to live in the core index as `packed_blobs`. The core resolve path
// overlays it through the Resolver capability; the core itself holds
// no pack tables and does not branch on pack-ness (closure, ADR-83).
//
// Storage is the backend-agnostic ExtensionStore — one table keyed by
// the packed ArtifactID, value = the encoded PlacementOverlay. The
// store is captured in Setup (db-mode after registration: committed
// reads, autocommit writes).
//
// Placement is recorded explicitly through RecordPack (ADR-86,
// decision A: the per-member entries are not carried on index events,
// so the bundler exposes its own write API rather than reacting to
// EventKindManifestIndexed). Because the map is a derived cache
// reconstructible from a volume's TOC-blob, RecordPack need not be
// transactionally atomic with the container manifest write.
//
// TODO(M4): Setup rebuilds the map from each volume's TOC-blob
// (recovery); the GCParticipant/Compactor capabilities (dead-ratio
// accounting and copy-forward compaction, ADR-86) land with the pack
// GC contract. This stub implements only the Resolver overlay.
type indexExtension struct {
	store extension.ExtensionStore
}

const (
	extName        = "scrinium.bundler"
	extSchemaVer   = 1
	placementTable = "placement"
)

// compile-time capability assertions.
var (
	_ extension.IndexExtension = (*indexExtension)(nil)
	_ extension.Resolver       = (*indexExtension)(nil)
)

// NewIndexExtension returns the bundler's index-side extension.
// Register it against a StoreIndex backend (extension.ExtensionHost)
// to give the core a Resolver overlay for packed artifacts.
func NewIndexExtension() extension.IndexExtension {
	return &indexExtension{}
}

func (e *indexExtension) Name() string { return extName }

func (e *indexExtension) SchemaVersion() int { return extSchemaVer }

// Subscribe returns no event kinds: the placement map is populated
// through RecordPack, not derived from index mutations (the per-member
// entries are not present in EventArgs).
func (e *indexExtension) Subscribe() []extension.EventKind { return nil }

// Setup captures the long-lived store for the read/write API. The
// stored value flips to db-mode once registration commits.
//
// TODO(M4): when oldVersion indicates an existing map, or on a cold
// start, rebuild placement by scanning each volume's TOC-blob.
func (e *indexExtension) Setup(ctx context.Context, store extension.ExtensionStore, oldVersion int) error {
	e.store = store
	return nil
}

// Apply is a no-op: this extension has no subscriptions.
func (e *indexExtension) Apply(ctx context.Context, store extension.ExtensionStore, kind extension.EventKind, args extension.EventArgs) error {
	return nil
}

func (e *indexExtension) Close() error { return nil }

// RecordPack records the placement of every member of a sealed pack
// volume (ADR-86, decision A). The container manifest is indexed by
// the core as an ordinary headless manifest — its two blob_refs (TOC
// + body) flow through manifest_blobs and carry the body blob's
// ref_count — so RecordPack adds only the per-member slice map this
// extension owns. The pack volume's blob_ref is the container's body
// blob (container.BlobRef).
func (e *indexExtension) RecordPack(ctx context.Context, container domain.Manifest, entries []PackedEntry) error {
	if e.store == nil {
		return fmt.Errorf("bundler: RecordPack before Setup")
	}
	packBlobRef := string(container.BlobRef)
	for _, entry := range entries {
		ov := extension.PlacementOverlay{
			PackBlobRef:    packBlobRef,
			ManifestOffset: entry.ManifestOffset,
			ManifestSize:   entry.ManifestSize,
			BlobOffset:     entry.BlobOffset,
			BlobSize:       entry.BlobSize,
			PipelineParams: entry.PipelineParams,
		}
		value, err := json.Marshal(ov)
		if err != nil {
			return fmt.Errorf("bundler: encode placement for %q: %w", entry.ArtifactID, err)
		}
		if err := e.store.Put(placementTable, string(entry.ArtifactID), value); err != nil {
			return fmt.Errorf("bundler: record placement for %q: %w", entry.ArtifactID, err)
		}
	}
	return nil
}

// ResolvePacked implements extension.Resolver: it overlays the
// placement of a packed artifact by its ArtifactID. A false return
// means the artifact is not packed (the caller falls back to россыпь).
func (e *indexExtension) ResolvePacked(ctx context.Context, artifactID domain.ArtifactID) (extension.PlacementOverlay, bool, error) {
	if e.store == nil {
		return extension.PlacementOverlay{}, false, fmt.Errorf("bundler: ResolvePacked before Setup")
	}
	value, ok, err := e.store.Get(placementTable, string(artifactID))
	if err != nil {
		return extension.PlacementOverlay{}, false, fmt.Errorf("bundler: lookup placement for %q: %w", artifactID, err)
	}
	if !ok {
		return extension.PlacementOverlay{}, false, nil
	}
	var ov extension.PlacementOverlay
	if err := json.Unmarshal(value, &ov); err != nil {
		return extension.PlacementOverlay{}, false, fmt.Errorf("bundler: decode placement for %q: %w", artifactID, err)
	}
	return ov, true, nil
}

// DeletePack removes the placement records of every member of the
// named pack volume — the owner-side counterpart of the core's old
// DeletePacked. Members are keyed by ArtifactID, so the volume's
// entries are found by scanning the placement table and matching
// PackBlobRef. Used by compaction/tombstoning (ADR-86); physical
// reclaim of the volume itself is the Compactor's job.
func (e *indexExtension) DeletePack(ctx context.Context, packBlobRef string) error {
	if e.store == nil {
		return fmt.Errorf("bundler: DeletePack before Setup")
	}
	var victims []string
	scanErr := e.store.Scan(placementTable, "", func(key string, value []byte) error {
		var ov extension.PlacementOverlay
		if err := json.Unmarshal(value, &ov); err != nil {
			return fmt.Errorf("bundler: decode placement at %q: %w", key, err)
		}
		if ov.PackBlobRef == packBlobRef {
			victims = append(victims, key)
		}
		return nil
	})
	if scanErr != nil {
		return scanErr
	}
	for _, key := range victims {
		if err := e.store.Delete(placementTable, key); err != nil {
			return fmt.Errorf("bundler: delete placement %q: %w", key, err)
		}
	}
	return nil
}

// PackedEntry describes one member of a sealed .pack volume — the
// bundler's write-API input to RecordPack (ADR-86). Relocated from
// domain: the core no longer knows pack types (the placement map is
// owned here, not in the core index).
type PackedEntry struct {
	ArtifactID     domain.ArtifactID
	BlobRef        string
	ManifestOffset int64
	ManifestSize   int64
	BlobOffset     int64
	BlobSize       int64

	ContentHash domain.ContentHash
	// CryptoIdentity carries the packed blob's crypto-identity (ADR-58)
	// so the dedup key survives packing. The bundler transfers it from
	// the source blob — it packs the finished ciphertext bytes as-is and
	// never re-encrypts, so the identity is unchanged. Empty for a Plain
	// packed blob. Kept in the bundler's placement map; pack-layer dedup
	// (M4) reads it.
	CryptoIdentity domain.CryptoIdentity
	Namespace      string
	SessionID      domain.SessionID
	PipelineParams []byte
}

package bundler

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/customindex"
)

// customIndex is the bundler's index-side half (ADR-88/86): it
// OWNS the placement map of packed artifacts — the table that used
// to live in the core index as `packed_blobs`. The core resolve path
// overlays it through the Resolver capability; the core itself holds
// no pack tables and does not branch on pack-ness (closure, ADR-83).
//
// Storage is the backend-agnostic Substrate — one table keyed by
// the packed ArtifactID, value = the encoded PlacementOverlay. The
// sub is captured in Setup (db-mode after registration: committed
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
type customIndex struct {
	sub customindex.Substrate
}

const (
	ciName          = "scrinium.bundler"
	ciSchemaVersion = 1
	placementTable  = "placement"
)

// compile-time capability assertions.
var (
	_ customindex.CustomIndex = (*customIndex)(nil)
	_ customindex.Resolver    = (*customIndex)(nil)
)

// NewCustomIndex returns the bundler's index-side customindex.
// Register it against a StoreIndex backend (customindex.Host)
// to give the core a Resolver overlay for packed artifacts.
func NewCustomIndex() customindex.CustomIndex {
	return &customIndex{}
}

func (e *customIndex) Name() string { return ciName }

func (e *customIndex) SchemaVersion() int { return ciSchemaVersion }

// Subscribe returns no event kinds: the placement map is populated
// through RecordPack, not derived from index mutations (the per-member
// entries are not present in EventArgs).
func (e *customIndex) Subscribe() []customindex.EventKind { return nil }

// Setup captures the long-lived sub for the read/write API. The
// stored value flips to db-mode once registration commits.
//
// TODO(M4): when oldVersion indicates an existing map, or on a cold
// start, rebuild placement by scanning each volume's TOC-blob.
func (e *customIndex) Setup(ctx context.Context, sub customindex.Substrate, oldVersion int) error {
	e.sub = sub
	return nil
}

// Apply is a no-op: this custom index has no subscriptions.
func (e *customIndex) Apply(ctx context.Context, sub customindex.Substrate, kind customindex.EventKind, args customindex.EventArgs) error {
	return nil
}

func (e *customIndex) Close() error { return nil }

// RecordPack records the placement of every member of a sealed pack
// volume (ADR-86, decision A). The container manifest is indexed by
// the core as an ordinary headless manifest — its two blob_refs (TOC
// + body) flow through manifest_blobs and carry the body blob's
// ref_count — so RecordPack adds only the per-member slice map this
// custom index owns. The pack volume's blob_ref is the container's body
// blob (container.BlobRef).
func (e *customIndex) RecordPack(ctx context.Context, container domain.Manifest, entries []PackedEntry) error {
	if e.sub == nil {
		return fmt.Errorf("bundler: RecordPack before Setup")
	}
	packBlobRef := string(container.PrimaryBlobRef())
	for _, entry := range entries {
		ov := customindex.PlacementOverlay{
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
		if err := e.sub.Put(placementTable, string(entry.ArtifactID), value); err != nil {
			return fmt.Errorf("bundler: record placement for %q: %w", entry.ArtifactID, err)
		}
	}
	return nil
}

// ResolvePacked implements customindex.Resolver: it overlays the
// placement of a packed artifact by its ArtifactID. A false return
// means the artifact is not packed (the caller falls back to plain).
func (e *customIndex) ResolvePacked(ctx context.Context, artifactID domain.ArtifactID) (customindex.PlacementOverlay, bool, error) {
	if e.sub == nil {
		return customindex.PlacementOverlay{}, false, fmt.Errorf("bundler: ResolvePacked before Setup")
	}
	value, ok, err := e.sub.Get(placementTable, string(artifactID))
	if err != nil {
		return customindex.PlacementOverlay{}, false, fmt.Errorf("bundler: lookup placement for %q: %w", artifactID, err)
	}
	if !ok {
		return customindex.PlacementOverlay{}, false, nil
	}
	var ov customindex.PlacementOverlay
	if err := json.Unmarshal(value, &ov); err != nil {
		return customindex.PlacementOverlay{}, false, fmt.Errorf("bundler: decode placement for %q: %w", artifactID, err)
	}
	return ov, true, nil
}

// DeletePack removes the placement records of every member of the
// named pack volume — the owner-side counterpart of the core's old
// DeletePacked. Members are keyed by ArtifactID, so the volume's
// entries are found by scanning the placement table and matching
// PackBlobRef. Used by compaction/tombstoning (ADR-86); physical
// reclaim of the volume itself is the Compactor's job.
func (e *customIndex) DeletePack(ctx context.Context, packBlobRef string) error {
	if e.sub == nil {
		return fmt.Errorf("bundler: DeletePack before Setup")
	}
	var victims []string
	scanErr := e.sub.Scan(placementTable, "", func(key string, value []byte) error {
		var ov customindex.PlacementOverlay
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
		if err := e.sub.Delete(placementTable, key); err != nil {
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

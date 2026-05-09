package indextest

import (
	"testing"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/testutil/manifestfx"
)

// --- LookupPacked ---

func runLookupPacked(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		packManifest := domain.Manifest{
			ArtifactID:   "pack-1",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('p'),
			BlobRef:      "pack-blob-1",
			OriginalSize: 65536,
			CreatedAt:    time.Now(),
		}
		entries := []domain.PackedEntry{{
			ArtifactID:     "art-p1",
			BlobRef:        "blob-p1",
			ManifestOffset: 0,
			ManifestSize:   200,
			BlobOffset:     200,
			BlobSize:       1024,
			ContentHash:    manifestfx.SyntheticHash('1'),
			PipelineParams: []byte{0xde, 0xad, 0xbe, 0xef},
		}}
		if err := idx.IndexManifest(ctx, packManifest, manifestfx.PhysAddr("packs/pack-1"), nil, entries); err != nil {
			t.Fatalf("setup: %v", err)
		}

		info, ok, err := idx.LookupPacked(ctx, "art-p1")
		if err != nil {
			t.Fatalf("LookupPacked: %v", err)
		}
		if !ok {
			t.Fatal("expected packed entry to be found")
		}
		if info.PackBlobRef != "pack-blob-1" {
			t.Errorf("PackBlobRef: got %q, want pack-blob-1", info.PackBlobRef)
		}
		if info.ManifestOffset != 0 || info.ManifestSize != 200 {
			t.Errorf("manifest range: got [%d, %d), want [0, 200)",
				info.ManifestOffset, info.ManifestSize)
		}
		if info.BlobOffset != 200 || info.BlobSize != 1024 {
			t.Errorf("blob range: got [%d, %d), want [200, 1024)",
				info.BlobOffset, info.BlobSize)
		}
		if len(info.PipelineParams) != 4 || info.PipelineParams[0] != 0xde {
			t.Errorf("PipelineParams round-trip lost bytes: got %v", info.PipelineParams)
		}
	})

	t.Run("Miss", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		_, ok, err := idx.LookupPacked(ctx, "not-packed")
		if err != nil {
			t.Fatalf("LookupPacked: %v", err)
		}
		if ok {
			t.Error("expected not found for non-packed artifact")
		}
	})
}

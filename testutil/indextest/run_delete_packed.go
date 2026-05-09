package indextest

import (
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/testutil/manifestfx"
)

// --- DeletePacked ---

func runDeletePacked(t *testing.T, f Factory) {
	t.Run("RemovesAllEntriesForOnePack", func(t *testing.T) {
		ctx := t.Context()
		// Stage two packs with their entries; DeletePacked of
		// pack-1 must clear pack-1's entries while pack-2's
		// stay reachable through LookupPacked.
		idx := f.New(t)

		pack1 := domain.Manifest{
			ArtifactID:   "pack-1",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('1'),
			BlobRef:      "pack-blob-1",
			OriginalSize: 4096,
			CreatedAt:    time.Now(),
		}
		if err := idx.IndexManifest(ctx, pack1, manifestfx.PhysAddr("packs/p1"), nil, []domain.PackedEntry{
			{ArtifactID: "a1", BlobRef: "b1", BlobSize: 100, ContentHash: manifestfx.SyntheticHash('a'), PipelineParams: []byte{}},
			{ArtifactID: "a2", BlobRef: "b2", BlobSize: 200, ContentHash: manifestfx.SyntheticHash('b'), PipelineParams: []byte{}},
		}); err != nil {
			t.Fatalf("setup pack-1: %v", err)
		}

		pack2 := domain.Manifest{
			ArtifactID:   "pack-2",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('2'),
			BlobRef:      "pack-blob-2",
			OriginalSize: 4096,
			CreatedAt:    time.Now(),
		}
		if err := idx.IndexManifest(ctx, pack2, manifestfx.PhysAddr("packs/p2"), nil, []domain.PackedEntry{
			{ArtifactID: "c1", BlobRef: "d1", BlobSize: 300, ContentHash: manifestfx.SyntheticHash('c'), PipelineParams: []byte{}},
		}); err != nil {
			t.Fatalf("setup pack-2: %v", err)
		}

		if err := idx.DeletePacked(ctx, "pack-blob-1"); err != nil {
			t.Fatalf("DeletePacked: %v", err)
		}

		// pack-1 entries gone.
		for _, id := range []domain.ArtifactID{"a1", "a2"} {
			_, ok, err := idx.LookupPacked(ctx, id)
			if err != nil {
				t.Fatalf("LookupPacked(%s): %v", id, err)
			}
			if ok {
				t.Errorf("LookupPacked(%s) still finds entry after DeletePacked", id)
			}
		}
		// pack-2 entry still there.
		_, ok, err := idx.LookupPacked(ctx, "c1")
		if err != nil {
			t.Fatalf("LookupPacked(c1): %v", err)
		}
		if !ok {
			t.Error("pack-2 entry c1 must survive DeletePacked(pack-1)")
		}
	})

	t.Run("Idempotent", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		if err := idx.DeletePacked(ctx, "nonexistent-pack"); err != nil {
			t.Errorf("DeletePacked of unknown pack must be no-op, got %v", err)
		}
	})
}

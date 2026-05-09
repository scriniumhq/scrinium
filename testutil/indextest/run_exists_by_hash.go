package indextest

import (
	"testing"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/testutil/manifestfx"
)

// --- ExistsByHash ---

func runExistsByHash(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('a')
		m := manifestfx.BlobWithHash("art-1", "blob-1", hash, 1024)
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}

		status, err := idx.ExistsByHash(ctx, hash)
		if err != nil {
			t.Fatalf("ExistsByHash: %v", err)
		}
		if status != domain.BlobExists {
			t.Errorf("status: got %d, want BlobExists", status)
		}
	})

	t.Run("Miss", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		status, err := idx.ExistsByHash(ctx, "sha256-deadbeef")
		if err != nil {
			t.Fatalf("ExistsByHash: %v", err)
		}
		if status != domain.BlobNotFound {
			t.Errorf("status: got %d, want BlobNotFound", status)
		}
	})

	t.Run("IgnoresSize", func(t *testing.T) {
		ctx := t.Context()
		// chunker.Wrapper does not know the size up front when
		// asking "have we seen this content before?". Two blobs
		// sharing a content_hash must both surface as BlobExists
		// regardless of size differences.
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('x')
		if err := idx.IndexManifest(ctx,
			manifestfx.BlobWithHash("art-1k", "blob-1k", hash, 1024),
			manifestfx.PhysAddr("p1"), nil, nil,
		); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(ctx,
			manifestfx.BlobWithHash("art-2k", "blob-2k", hash, 2048),
			manifestfx.PhysAddr("p2"), nil, nil,
		); err != nil {
			t.Fatal(err)
		}

		status, err := idx.ExistsByHash(ctx, hash)
		if err != nil {
			t.Fatalf("ExistsByHash: %v", err)
		}
		if status != domain.BlobExists {
			t.Errorf("status: got %d, want BlobExists", status)
		}
	})
}

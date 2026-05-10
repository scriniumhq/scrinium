package indextest

import (
	"testing"

	"scrinium.dev/internal/testutil/manifestfx"
)

// --- ExistsByContent ---

func runExistsByContent(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('a')
		m := manifestfx.BlobWithHash("art-1", "blob-1", hash, 1024)
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("blobs/blob-1"), nil, nil); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		ref, ok, err := idx.ExistsByContent(ctx, hash, 1024)
		if err != nil {
			t.Fatalf("ExistsByContent: %v", err)
		}
		if !ok {
			t.Fatal("expected found")
		}
		if ref != "blob-1" {
			t.Errorf("ref: got %q, want %q", ref, "blob-1")
		}
	})

	t.Run("Miss", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		ref, ok, err := idx.ExistsByContent(ctx, "sha256-deadbeef", 999)
		if err != nil {
			t.Fatalf("ExistsByContent: %v", err)
		}
		if ok {
			t.Error("expected not found")
		}
		if ref != "" {
			t.Errorf("ref: got %q, want empty", ref)
		}
	})

	t.Run("HashHitSizeMiss", func(t *testing.T) {
		ctx := t.Context()
		// The composite key (content_hash, original_size) is
		// strict: same hash, different size — distinct entries,
		// not matches.
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('x')
		m := manifestfx.BlobWithHash("art-1k", "blob-1k", hash, 1024)
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p1"), nil, nil); err != nil {
			t.Fatal(err)
		}

		_, ok, err := idx.ExistsByContent(ctx, hash, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("hash-only match leaked through size filter")
		}
	})
}

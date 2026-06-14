package indextest

import (
	"encoding/json"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/testutil/manifestfx"
)

// --- IndexManifest ---

func runIndexManifest(t *testing.T, f Factory) {
	t.Run("Blob_FreshInsert", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("blobs/aa/bb/blob-1"), nil, nil); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}
		// Manifest visible.
		exists, err := idx.ManifestExists(ctx, "art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("manifest must be visible after IndexManifest")
		}
		// Blob has a ref.
		n, err := idx.GetRefCount(ctx, "blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("ref_count: got %d, want 1", n)
		}
		// Blob is resolvable.
		if _, err := idx.Resolve(ctx, "blob-1"); err != nil {
			t.Errorf("Resolve after Index: %v", err)
		}
	})

	t.Run("Blob_Dedup", func(t *testing.T) {
		ctx := t.Context()
		// Two distinct artifacts referencing the same blob —
		// blob row stays single, ref_count climbs to 2.
		idx := f.New(t)
		addr := manifestfx.PhysAddr("blobs/aa/bb/blob-1")
		if err := idx.IndexManifest(ctx, manifestfx.Blob("art-1", "blob-1"), addr, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(ctx, manifestfx.Blob("art-2", "blob-1"), addr, nil, nil); err != nil {
			t.Fatal(err)
		}
		n, err := idx.GetRefCount(ctx, "blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("ref_count: got %d, want 2", n)
		}
	})

	t.Run("Blob_Idempotent", func(t *testing.T) {
		ctx := t.Context()
		// Re-indexing the same artifact (same ID, same blobRef)
		// must not fail. Manifest-row uniqueness is the strict
		// invariant; ref_count behaviour on retries is an
		// implementation detail covered by the per-backend tests.
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatalf("re-indexing same manifest must not fail: %v", err)
		}
		exists, err := idx.ManifestExists(ctx, "art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("manifest disappeared on second IndexManifest")
		}
	})

	t.Run("TOC_RegistersChunks", func(t *testing.T) {
		ctx := t.Context()
		// A TOC manifest pulls together previously-registered
		// chunk blobs. Each chunk's ref_count climbs by one; the
		// TOC's own blob (the manifest body) is also a regular
		// blob with its own ref_count.
		idx := f.New(t)

		chunks := []struct {
			ref  string
			hash domain.ContentHash
		}{
			{"chunk-a", manifestfx.SyntheticHash('a')},
			{"chunk-b", manifestfx.SyntheticHash('b')},
			{"chunk-c", manifestfx.SyntheticHash('c')},
		}
		// Register chunks as blobs first, each via its own
		// IndexManifest call. The manifest is artificial — what
		// matters for the TOC test below is that the blob row
		// exists.
		for i, c := range chunks {
			m := manifestfx.BlobWithHash(
				"chunk-mf-"+c.ref,
				c.ref,
				c.hash,
				1024,
			)
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("chunks/"+c.ref), nil, nil); err != nil {
				t.Fatalf("seed chunk %d: %v", i, err)
			}
		}

		toc := domain.Manifest{
			ArtifactID:   "art-toc",
			Ext:          json.RawMessage(`{"composite":true}`),
			Namespace:    "test",
			ContentHash:  manifestfx.SyntheticHash('0'),
			BlobRef:      "toc-blob",
			OriginalSize: 3072,
			CreatedAt:    time.Now(),
		}
		chunkRefs := []string{chunks[0].ref, chunks[1].ref, chunks[2].ref}
		if err := idx.IndexManifest(ctx, toc, manifestfx.PhysAddr("blobs/toc-blob"), chunkRefs, nil); err != nil {
			t.Fatalf("IndexManifest TOC: %v", err)
		}

		// Each chunk now ref-counted: 1 (from its own manifest)
		// + 1 (from the TOC chunkRefs) = 2.
		for _, c := range chunks {
			n, err := idx.GetRefCount(ctx, c.ref)
			if err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Errorf("chunk %s ref_count: got %d, want 2", c.ref, n)
			}
		}
		// TOC blob: 1.
		n, err := idx.GetRefCount(ctx, "toc-blob")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("toc-blob ref_count: got %d, want 1", n)
		}
	})

	t.Run("TOC_MissingChunkFails", func(t *testing.T) {
		ctx := t.Context()
		// A TOC pointing at a chunk that was never registered
		// must fail. The manifest must NOT appear in the index
		// (the call rolls back).
		idx := f.New(t)
		toc := domain.Manifest{
			ArtifactID:   "art-toc",
			Ext:          json.RawMessage(`{"composite":true}`),
			Namespace:    "test",
			ContentHash:  manifestfx.SyntheticHash('0'),
			BlobRef:      "toc-blob",
			OriginalSize: 3072,
			CreatedAt:    time.Now(),
		}
		err := idx.IndexManifest(ctx, toc, manifestfx.PhysAddr("p"), []string{"chunk-missing"}, nil)
		if err == nil {
			t.Fatal("expected error on missing chunk")
		}
		exists, qerr := idx.ManifestExists(ctx, "art-toc")
		if qerr != nil {
			t.Fatalf("ManifestExists post-rollback: %v", qerr)
		}
		if exists {
			t.Error("manifest leaked into index after a failed TOC IndexManifest")
		}
	})
}

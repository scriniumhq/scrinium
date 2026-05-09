package indextest

import (
	"testing"

	"github.com/rkurbatov/scrinium/engine/internal/testutil/manifestfx"
)

// --- DeleteManifest ---

func runDeleteManifest(t *testing.T, f Factory) {
	t.Run("Blob_DropsRefCount", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest(ctx, "art-1", []string{"blob-1"}); err != nil {
			t.Fatalf("DeleteManifest: %v", err)
		}
		exists, err := idx.ManifestExists(ctx, "art-1")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("manifest still visible after DeleteManifest")
		}
		// Blob row remains as orphan with ref_count = 0 — the GC
		// state, not "missing".
		n, err := idx.GetRefCount(ctx, "blob-1")
		if err != nil {
			t.Fatalf("blob row gone (got %v); orphans must persist for GC", err)
		}
		if n != 0 {
			t.Errorf("ref_count: got %d, want 0", n)
		}
	})

	t.Run("Idempotent", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		if err := idx.DeleteManifest(ctx, "nonexistent", nil); err != nil {
			t.Errorf("delete of unknown artifact must be no-op, got %v", err)
		}
	})

	t.Run("BlobRefMismatch", func(t *testing.T) {
		ctx := t.Context()
		// Caller passes blobRefs that don't match the manifest's
		// linked blobs. The implementation must refuse and leave
		// the index unchanged.
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest(ctx, "art-1", []string{"blob-WRONG"}); err == nil {
			t.Fatal("expected error on blobRefs mismatch")
		}
		exists, err := idx.ManifestExists(ctx, "art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("manifest disappeared after a refused DeleteManifest")
		}
	})
}

package indextest

import (
	"context"
	"testing"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/testutil/manifestfx"
)

// --- RebindBlob ---

func runRebindBlob(t *testing.T, f Factory) {
	t.Run("Basic", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("transit/blob-1"), nil, nil); err != nil {
			t.Fatal(err)
		}
		// Initial address.
		got, err := idx.Resolve(ctx, "blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != "transit/blob-1" {
			t.Fatalf("initial path: got %q, want transit/blob-1", got.Path)
		}

		// Rebind to a Location-workspace path.
		newAddr := manifestfx.PhysAddr("blobs/aa/bb/blob-1")
		if err := idx.RebindBlob(context.Background(), "blob-1", newAddr); err != nil {
			t.Fatalf("RebindBlob: %v", err)
		}
		got, err = idx.Resolve(ctx, "blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != "blobs/aa/bb/blob-1" {
			t.Errorf("rebind path: got %q, want blobs/aa/bb/blob-1", got.Path)
		}
		if got.Workspace != domain.WorkspaceLocation {
			t.Errorf("workspace: got %d, want %d", got.Workspace, domain.WorkspaceLocation)
		}
		// ref_count untouched.
		n, err := idx.GetRefCount(ctx, "blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("ref_count after rebind: got %d, want 1", n)
		}
	})

	t.Run("MissingBlobIsNoOp", func(t *testing.T) {
		idx := f.New(t)
		err := idx.RebindBlob(context.Background(), "nonexistent",
			manifestfx.PhysAddr("p"))
		if err != nil {
			t.Errorf("missing blob must be no-op, got %v", err)
		}
	})
}

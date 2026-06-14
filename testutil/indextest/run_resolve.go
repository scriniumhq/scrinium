package indextest

import (
	"errors"
	"testing"

	"scrinium.dev/errs"
	"scrinium.dev/testutil/manifestfx"
)

// --- Resolve ---

func runResolve(t *testing.T, f Factory) {
	t.Run("Basic", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		addr := manifestfx.PhysAddr("blobs/aa/bb/blob-1")
		if err := idx.IndexManifest(ctx, m, addr); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		got, err := idx.Resolve(ctx, "blob-1")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Path != "blobs/aa/bb/blob-1" {
			t.Errorf("Path: got %q, want %q", got.Path, "blobs/aa/bb/blob-1")
		}
	})

	t.Run("Missing", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		_, err := idx.Resolve(ctx, "nonexistent")
		if !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
		}
	})
}

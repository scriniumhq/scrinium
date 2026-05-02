package indextest

import (
	"errors"
	"testing"

	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/manifestfx"
)

// --- GetRefCount ---

func runGetRefCount(t *testing.T, f Factory) {
	t.Run("Basic", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}

		n, err := idx.GetRefCount("blob-1")
		if err != nil {
			t.Fatalf("GetRefCount: %v", err)
		}
		if n != 1 {
			t.Errorf("ref_count: got %d, want 1", n)
		}
	})

	t.Run("Missing", func(t *testing.T) {
		idx := f.New(t)
		_, err := idx.GetRefCount("nonexistent")
		if !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
		}
	})

	t.Run("Zero", func(t *testing.T) {
		// "Missing" and "ref_count = 0" are distinct states: the
		// latter is a legitimate orphan kept for the GC reaper
		// to process. Reach it through Index → Delete.
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest("art-1", []string{"blob-1"}); err != nil {
			t.Fatal(err)
		}

		n, err := idx.GetRefCount("blob-1")
		if err != nil {
			t.Fatalf("GetRefCount: %v", err)
		}
		if n != 0 {
			t.Errorf("ref_count: got %d, want 0", n)
		}
	})
}

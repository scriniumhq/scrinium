package indextest

import (
	"strings"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/internal/testutil/manifestfx"
)

// --- ManifestExists ---

func runManifestExists(t *testing.T, f Factory) {
	t.Run("Fresh_ReturnsFalse", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		exists, err := idx.ManifestExists(ctx, domain.ArtifactID("sha256-"+strings.Repeat("a", 64)))
		if err != nil {
			t.Fatalf("ManifestExists: %v", err)
		}
		if exists {
			t.Error("fresh index must report ManifestExists = false")
		}
	})

	t.Run("AfterIndex_ReturnsTrue", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		exists, err := idx.ManifestExists(ctx, "art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("ManifestExists must be true after IndexManifest")
		}
	})

	t.Run("AfterDelete_ReturnsFalse", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-2", "blob-2")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest(ctx, "art-2", []string{"blob-2"}); err != nil {
			t.Fatal(err)
		}
		exists, err := idx.ManifestExists(ctx, "art-2")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("ManifestExists must be false after DeleteManifest")
		}
	})

	t.Run("DistinguishesIDs", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.Blob("art-known", "blob-known")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		known, err := idx.ManifestExists(ctx, "art-known")
		if err != nil {
			t.Fatal(err)
		}
		if !known {
			t.Error("ManifestExists(known) = false, want true")
		}
		unknown, err := idx.ManifestExists(ctx, "art-unknown")
		if err != nil {
			t.Fatal(err)
		}
		if unknown {
			t.Error("ManifestExists(unknown) = true, want false")
		}
	})

	t.Run("NotConfusedByBlobRef", func(t *testing.T) {
		ctx := t.Context()
		// ManifestExists must look in the manifests-table only,
		// not the blobs-table. Probe with a blob_ref-shaped
		// string that is NOT an ArtifactID.
		idx := f.New(t)
		m := manifestfx.Blob("art-real", "blob-real")
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		exists, err := idx.ManifestExists(ctx, "blob-real")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("ManifestExists must not match blob refs")
		}
	})
}

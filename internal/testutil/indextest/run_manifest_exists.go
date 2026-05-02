package indextest

import (
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/testutil/manifestfx"
)

// --- ManifestExists ---

func runManifestExists(t *testing.T, f Factory) {
	t.Run("Fresh_ReturnsFalse", func(t *testing.T) {
		idx := f.New(t)
		exists, err := idx.ManifestExists(domain.ArtifactID("sha256-" + strings.Repeat("a", 64)))
		if err != nil {
			t.Fatalf("ManifestExists: %v", err)
		}
		if exists {
			t.Error("fresh index must report ManifestExists = false")
		}
	})

	t.Run("AfterIndex_ReturnsTrue", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		exists, err := idx.ManifestExists("art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("ManifestExists must be true after IndexManifest")
		}
	})

	t.Run("AfterDelete_ReturnsFalse", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-2", "blob-2")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest("art-2", []string{"blob-2"}); err != nil {
			t.Fatal(err)
		}
		exists, err := idx.ManifestExists("art-2")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("ManifestExists must be false after DeleteManifest")
		}
	})

	t.Run("DistinguishesIDs", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-known", "blob-known")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		known, err := idx.ManifestExists("art-known")
		if err != nil {
			t.Fatal(err)
		}
		if !known {
			t.Error("ManifestExists(known) = false, want true")
		}
		unknown, err := idx.ManifestExists("art-unknown")
		if err != nil {
			t.Fatal(err)
		}
		if unknown {
			t.Error("ManifestExists(unknown) = true, want false")
		}
	})

	t.Run("NotConfusedByBlobRef", func(t *testing.T) {
		// ManifestExists must look in the manifests-table only,
		// not the blobs-table. Probe with a blob_ref-shaped
		// string that is NOT an ArtifactID.
		idx := f.New(t)
		m := manifestfx.Blob("art-real", "blob-real")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		exists, err := idx.ManifestExists("blob-real")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("ManifestExists must not match blob refs")
		}
	})
}

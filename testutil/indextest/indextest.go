package indextest

import (
	"context"
	"testing"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/domain"
)

// Factory describes one StoreIndex implementation under test.
type Factory struct {
	// Name appears in subtest output as a prefix. The suite uses
	// t.Run(Name+"/"+caseName) so multiple factories can be
	// exercised from the same test entry point if ever needed.
	Name string

	// New returns a fresh, empty StoreIndex. Each subtest gets its
	// own instance — implementations should rely on t.Cleanup for
	// teardown and never share state across subtests.
	New func(t *testing.T) core.StoreIndex
}

// Run executes the full conformance suite against f.
func Run(t *testing.T, f Factory) {
	t.Helper()
	if f.New == nil {
		t.Fatal("indextest.Run: Factory.New is nil")
	}
	if f.Name == "" {
		f.Name = "anon"
	}

	// Group the suite into logical sections. Each section is its
	// own t.Run so a failure in one method's tests does not hide
	// failures elsewhere.
	t.Run(f.Name+"/Resolve", func(t *testing.T) { runResolve(t, f) })
	t.Run(f.Name+"/ExistsByContent", func(t *testing.T) { runExistsByContent(t, f) })
	t.Run(f.Name+"/ExistsByHash", func(t *testing.T) { runExistsByHash(t, f) })
	t.Run(f.Name+"/GetRefCount", func(t *testing.T) { runGetRefCount(t, f) })
	t.Run(f.Name+"/IndexManifest", func(t *testing.T) { runIndexManifest(t, f) })
	t.Run(f.Name+"/DeleteManifest", func(t *testing.T) { runDeleteManifest(t, f) })
	t.Run(f.Name+"/RebindBlob", func(t *testing.T) { runRebindBlob(t, f) })
	t.Run(f.Name+"/ManifestExists", func(t *testing.T) { runManifestExists(t, f) })
	t.Run(f.Name+"/LookupPacked", func(t *testing.T) { runLookupPacked(t, f) })
	t.Run(f.Name+"/MarkVerified", func(t *testing.T) { runMarkVerified(t, f) })
	t.Run(f.Name+"/DeletePacked", func(t *testing.T) { runDeletePacked(t, f) })
	t.Run(f.Name+"/ListByNamespace", func(t *testing.T) { runListByNamespace(t, f) })
	t.Run(f.Name+"/GetBySession", func(t *testing.T) { runGetBySession(t, f) })
	t.Run(f.Name+"/ListOrphanBlobs", func(t *testing.T) { runListOrphanBlobs(t, f) })
	t.Run(f.Name+"/ListUnverified", func(t *testing.T) { runListUnverified(t, f) })
}

// collectByNamespace is a small helper that turns a streaming
// ListByNamespace into a slice for table-style assertions. It is
// used by every run_X.go that needs to inspect the iteration
// order or contents of ListByNamespace results.
func collectByNamespace(t *testing.T, idx core.StoreIndex, ns string) []domain.Manifest {
	t.Helper()
	var got []domain.Manifest
	err := idx.ListByNamespace(context.Background(), ns, func(m domain.Manifest) error {
		got = append(got, m)
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace(%q): %v", ns, err)
	}
	return got
}

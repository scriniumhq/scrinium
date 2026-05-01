// Package indextest is the shared conformance suite for
// implementations of core.StoreIndex.
//
// Every implementation (index/sqlite, future index/postgres,
// future in-memory backends) is expected to register a Factory
// and call Run from its own _test.go. The suite exercises the
// public StoreIndex contract through black-box assertions only —
// no SQL, no implementation-specific table peeks.
//
// Tests that require glass-box visibility (verifying a SQL
// transaction rolled back, that a particular SQLITE_BUSY mapping
// returned the right errs sentinel, that NULL columns are handled
// the way the schema expects) stay in the implementation
// subpackage. They are not duplicates of conformance tests; they
// witness the same property through a stricter mechanism.
//
// Usage:
//
//	func TestConformance_SQLite(t *testing.T) {
//	    indextest.Run(t, indextest.Factory{
//	        Name: "sqlite-memory",
//	        New: func(t *testing.T) core.StoreIndex {
//	            idx, err := sqlite.NewStore(context.Background(), ":memory:")
//	            if err != nil { t.Fatal(err) }
//	            t.Cleanup(func() {
//	                if c, ok := idx.(io.Closer); ok { _ = c.Close() }
//	            })
//	            return idx
//	        },
//	    })
//	}
package indextest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/manifestfx"
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

// --- Resolve ---

func runResolve(t *testing.T, f Factory) {
	t.Run("Basic", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		addr := manifestfx.PhysAddr("blobs/aa/bb/blob-1")
		if err := idx.IndexManifest(m, addr, nil, nil); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		got, err := idx.Resolve("blob-1")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Workspace != domain.WorkspaceLocation {
			t.Errorf("Workspace: got %d, want %d", got.Workspace, domain.WorkspaceLocation)
		}
		if got.Path != "blobs/aa/bb/blob-1" {
			t.Errorf("Path: got %q, want %q", got.Path, "blobs/aa/bb/blob-1")
		}
	})

	t.Run("Missing", func(t *testing.T) {
		idx := f.New(t)
		_, err := idx.Resolve("nonexistent")
		if !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
		}
	})

	// Note: the "blob row with pack_ref/offset/size populated"
	// case is sqlite-specific glass-box behaviour. After
	// IndexManifest of a pack manifest, only ONE blobs row exists
	// (the pack blob itself); the embedded blobs live in
	// packed_blobs and are looked up via LookupPacked, not
	// Resolve. The packed-entries side is covered by
	// IndexManifest/Pack_RegistersEntries and LookupPacked/Hit
	// below.
}

// --- ExistsByContent ---

func runExistsByContent(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('a')
		m := manifestfx.BlobWithHash("art-1", "blob-1", hash, 1024)
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("blobs/blob-1"), nil, nil); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		ref, ok, err := idx.ExistsByContent(hash, 1024)
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
		idx := f.New(t)
		ref, ok, err := idx.ExistsByContent("sha256-deadbeef", 999)
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
		// The composite key (content_hash, original_size) is
		// strict: same hash, different size — distinct entries,
		// not matches.
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('x')
		m := manifestfx.BlobWithHash("art-1k", "blob-1k", hash, 1024)
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p1"), nil, nil); err != nil {
			t.Fatal(err)
		}

		_, ok, err := idx.ExistsByContent(hash, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("hash-only match leaked through size filter")
		}
	})
}

// --- ExistsByHash ---

func runExistsByHash(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('a')
		m := manifestfx.BlobWithHash("art-1", "blob-1", hash, 1024)
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}

		status, err := idx.ExistsByHash(hash)
		if err != nil {
			t.Fatalf("ExistsByHash: %v", err)
		}
		if status != domain.BlobExists {
			t.Errorf("status: got %d, want BlobExists", status)
		}
	})

	t.Run("Miss", func(t *testing.T) {
		idx := f.New(t)
		status, err := idx.ExistsByHash("sha256-deadbeef")
		if err != nil {
			t.Fatalf("ExistsByHash: %v", err)
		}
		if status != domain.BlobNotFound {
			t.Errorf("status: got %d, want BlobNotFound", status)
		}
	})

	t.Run("IgnoresSize", func(t *testing.T) {
		// chunker.Wrapper does not know the size up front when
		// asking "have we seen this content before?". Two blobs
		// sharing a content_hash must both surface as BlobExists
		// regardless of size differences.
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('x')
		if err := idx.IndexManifest(
			manifestfx.BlobWithHash("art-1k", "blob-1k", hash, 1024),
			manifestfx.PhysAddr("p1"), nil, nil,
		); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(
			manifestfx.BlobWithHash("art-2k", "blob-2k", hash, 2048),
			manifestfx.PhysAddr("p2"), nil, nil,
		); err != nil {
			t.Fatal(err)
		}

		status, err := idx.ExistsByHash(hash)
		if err != nil {
			t.Fatalf("ExistsByHash: %v", err)
		}
		if status != domain.BlobExists {
			t.Errorf("status: got %d, want BlobExists", status)
		}
	})
}

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

// --- IndexManifest ---

func runIndexManifest(t *testing.T, f Factory) {
	t.Run("Blob_FreshInsert", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("blobs/aa/bb/blob-1"), nil, nil); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}
		// Manifest visible.
		exists, err := idx.ManifestExists("art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("manifest must be visible after IndexManifest")
		}
		// Blob has a ref.
		n, err := idx.GetRefCount("blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("ref_count: got %d, want 1", n)
		}
		// Blob is resolvable.
		if _, err := idx.Resolve("blob-1"); err != nil {
			t.Errorf("Resolve after Index: %v", err)
		}
	})

	t.Run("Blob_Dedup", func(t *testing.T) {
		// Two distinct artifacts referencing the same blob —
		// blob row stays single, ref_count climbs to 2.
		idx := f.New(t)
		addr := manifestfx.PhysAddr("blobs/aa/bb/blob-1")
		if err := idx.IndexManifest(manifestfx.Blob("art-1", "blob-1"), addr, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(manifestfx.Blob("art-2", "blob-1"), addr, nil, nil); err != nil {
			t.Fatal(err)
		}
		n, err := idx.GetRefCount("blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("ref_count: got %d, want 2", n)
		}
	})

	t.Run("Blob_Idempotent", func(t *testing.T) {
		// Re-indexing the same artifact (same ID, same blobRef)
		// must not fail. Manifest-row uniqueness is the strict
		// invariant; ref_count behaviour on retries is an
		// implementation detail covered by the per-backend tests.
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatalf("re-indexing same manifest must not fail: %v", err)
		}
		exists, err := idx.ManifestExists("art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("manifest disappeared on second IndexManifest")
		}
	})

	t.Run("TOC_RegistersChunks", func(t *testing.T) {
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
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("chunks/"+c.ref), nil, nil); err != nil {
				t.Fatalf("seed chunk %d: %v", i, err)
			}
		}

		toc := domain.Manifest{
			ArtifactID:   "art-toc",
			Type:         domain.ManifestTypeTOC,
			Namespace:    "test",
			ContentHash:  manifestfx.SyntheticHash('0'),
			BlobRef:      "toc-blob",
			OriginalSize: 3072,
			CreatedAt:    time.Now(),
		}
		chunkRefs := []string{chunks[0].ref, chunks[1].ref, chunks[2].ref}
		if err := idx.IndexManifest(toc, manifestfx.PhysAddr("blobs/toc-blob"), chunkRefs, nil); err != nil {
			t.Fatalf("IndexManifest TOC: %v", err)
		}

		// Each chunk now ref-counted: 1 (from its own manifest)
		// + 1 (from the TOC chunkRefs) = 2.
		for _, c := range chunks {
			n, err := idx.GetRefCount(c.ref)
			if err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Errorf("chunk %s ref_count: got %d, want 2", c.ref, n)
			}
		}
		// TOC blob: 1.
		n, err := idx.GetRefCount("toc-blob")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("toc-blob ref_count: got %d, want 1", n)
		}
	})

	t.Run("TOC_MissingChunkFails", func(t *testing.T) {
		// A TOC pointing at a chunk that was never registered
		// must fail. The manifest must NOT appear in the index
		// (the call rolls back).
		idx := f.New(t)
		toc := domain.Manifest{
			ArtifactID:   "art-toc",
			Type:         domain.ManifestTypeTOC,
			Namespace:    "test",
			ContentHash:  manifestfx.SyntheticHash('0'),
			BlobRef:      "toc-blob",
			OriginalSize: 3072,
			CreatedAt:    time.Now(),
		}
		err := idx.IndexManifest(toc, manifestfx.PhysAddr("p"), []string{"chunk-missing"}, nil)
		if err == nil {
			t.Fatal("expected error on missing chunk")
		}
		exists, qerr := idx.ManifestExists("art-toc")
		if qerr != nil {
			t.Fatalf("ManifestExists post-rollback: %v", qerr)
		}
		if exists {
			t.Error("manifest leaked into index after a failed TOC IndexManifest")
		}
	})

	t.Run("Pack_RegistersEntries", func(t *testing.T) {
		// Pack manifests are invisible to ManifestExists/Walk —
		// that is the contract. What is observable: each packed
		// entry resolves to its packed location, and the pack
		// blob's ref_count equals the number of entries.
		idx := f.New(t)

		packManifest := domain.Manifest{
			ArtifactID:   "pack-1",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('p'),
			BlobRef:      "pack-blob-1",
			OriginalSize: 65536,
			CreatedAt:    time.Now(),
		}
		entries := []domain.PackedEntry{
			{
				ArtifactID:     "art-p1",
				BlobRef:        "blob-p1",
				ManifestOffset: 0,
				ManifestSize:   200,
				BlobOffset:     200,
				BlobSize:       1024,
				ContentHash:    manifestfx.SyntheticHash('1'),
				PipelineParams: []byte{},
			},
			{
				ArtifactID:     "art-p2",
				BlobRef:        "blob-p2",
				ManifestOffset: 1224,
				ManifestSize:   200,
				BlobOffset:     1424,
				BlobSize:       2048,
				ContentHash:    manifestfx.SyntheticHash('2'),
				PipelineParams: []byte{},
			},
		}
		if err := idx.IndexManifest(packManifest, manifestfx.PhysAddr("packs/pack-1"), nil, entries); err != nil {
			t.Fatalf("IndexManifest pack: %v", err)
		}

		// Pack-blob ref_count: one per packed artifact.
		n, err := idx.GetRefCount("pack-blob-1")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("pack-blob ref_count: got %d, want 2", n)
		}

		// Pack manifests are NOT in ManifestExists.
		packExists, err := idx.ManifestExists("pack-1")
		if err != nil {
			t.Fatal(err)
		}
		if packExists {
			t.Error("pack manifest leaked into ManifestExists")
		}

		// LookupPacked sees the entries.
		for _, e := range entries {
			info, ok, err := idx.LookupPacked(e.ArtifactID)
			if err != nil {
				t.Fatalf("LookupPacked(%s): %v", e.ArtifactID, err)
			}
			if !ok {
				t.Errorf("LookupPacked(%s): not found", e.ArtifactID)
				continue
			}
			if info.PackBlobRef != "pack-blob-1" {
				t.Errorf("LookupPacked(%s).PackBlobRef: got %q, want pack-blob-1",
					e.ArtifactID, info.PackBlobRef)
			}
		}
	})
}

// --- DeleteManifest ---

func runDeleteManifest(t *testing.T, f Factory) {
	t.Run("Blob_DropsRefCount", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest("art-1", []string{"blob-1"}); err != nil {
			t.Fatalf("DeleteManifest: %v", err)
		}
		exists, err := idx.ManifestExists("art-1")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("manifest still visible after DeleteManifest")
		}
		// Blob row remains as orphan with ref_count = 0 — the GC
		// state, not "missing".
		n, err := idx.GetRefCount("blob-1")
		if err != nil {
			t.Fatalf("blob row gone (got %v); orphans must persist for GC", err)
		}
		if n != 0 {
			t.Errorf("ref_count: got %d, want 0", n)
		}
	})

	t.Run("Idempotent", func(t *testing.T) {
		idx := f.New(t)
		if err := idx.DeleteManifest("nonexistent", nil); err != nil {
			t.Errorf("delete of unknown artifact must be no-op, got %v", err)
		}
	})

	t.Run("BlobRefMismatch", func(t *testing.T) {
		// Caller passes blobRefs that don't match the manifest's
		// linked blobs. The implementation must refuse and leave
		// the index unchanged.
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.DeleteManifest("art-1", []string{"blob-WRONG"}); err == nil {
			t.Fatal("expected error on blobRefs mismatch")
		}
		exists, err := idx.ManifestExists("art-1")
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Error("manifest disappeared after a refused DeleteManifest")
		}
	})
}

// --- RebindBlob ---

func runRebindBlob(t *testing.T, f Factory) {
	t.Run("Basic", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("transit/blob-1"), nil, nil); err != nil {
			t.Fatal(err)
		}
		// Initial address.
		got, err := idx.Resolve("blob-1")
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
		got, err = idx.Resolve("blob-1")
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
		n, err := idx.GetRefCount("blob-1")
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

// --- LookupPacked ---

func runLookupPacked(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		idx := f.New(t)
		packManifest := domain.Manifest{
			ArtifactID:   "pack-1",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('p'),
			BlobRef:      "pack-blob-1",
			OriginalSize: 65536,
			CreatedAt:    time.Now(),
		}
		entries := []domain.PackedEntry{{
			ArtifactID:     "art-p1",
			BlobRef:        "blob-p1",
			ManifestOffset: 0,
			ManifestSize:   200,
			BlobOffset:     200,
			BlobSize:       1024,
			ContentHash:    manifestfx.SyntheticHash('1'),
			PipelineParams: []byte{0xde, 0xad, 0xbe, 0xef},
		}}
		if err := idx.IndexManifest(packManifest, manifestfx.PhysAddr("packs/pack-1"), nil, entries); err != nil {
			t.Fatalf("setup: %v", err)
		}

		info, ok, err := idx.LookupPacked("art-p1")
		if err != nil {
			t.Fatalf("LookupPacked: %v", err)
		}
		if !ok {
			t.Fatal("expected packed entry to be found")
		}
		if info.PackBlobRef != "pack-blob-1" {
			t.Errorf("PackBlobRef: got %q, want pack-blob-1", info.PackBlobRef)
		}
		if info.ManifestOffset != 0 || info.ManifestSize != 200 {
			t.Errorf("manifest range: got [%d, %d), want [0, 200)",
				info.ManifestOffset, info.ManifestSize)
		}
		if info.BlobOffset != 200 || info.BlobSize != 1024 {
			t.Errorf("blob range: got [%d, %d), want [200, 1024)",
				info.BlobOffset, info.BlobSize)
		}
		if len(info.PipelineParams) != 4 || info.PipelineParams[0] != 0xde {
			t.Errorf("PipelineParams round-trip lost bytes: got %v", info.PipelineParams)
		}
	})

	t.Run("Miss", func(t *testing.T) {
		idx := f.New(t)
		_, ok, err := idx.LookupPacked("not-packed")
		if err != nil {
			t.Fatalf("LookupPacked: %v", err)
		}
		if ok {
			t.Error("expected not found for non-packed artifact")
		}
	})
}

// --- MarkVerified ---

func runMarkVerified(t *testing.T, f Factory) {
	t.Run("UpdatesObservableThroughListUnverified", func(t *testing.T) {
		// MarkVerified updates last_verified_at on a blob.
		// Without poking the storage, we observe it through
		// ListUnverified: a blob freshly indexed has NULL
		// last_verified_at and is reported by every
		// ListUnverified call; after MarkVerified with a recent
		// timestamp, the same call with `before` set to a moment
		// before the verification stops reporting it.
		idx := f.New(t)
		m := manifestfx.Blob("art-1", "blob-1")
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}

		// Truncate to seconds (RFC 3339 storage precision).
		verifiedAt := time.Now().UTC().Truncate(time.Second)
		if err := idx.MarkVerified("blob-1", verifiedAt); err != nil {
			t.Fatalf("MarkVerified: %v", err)
		}

		// `before` strictly older than verifiedAt — blob must
		// NOT appear (it has been verified more recently).
		var seen []string
		err := idx.ListUnverified(context.Background(), verifiedAt.Add(-time.Minute), func(ref string) error {
			seen = append(seen, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListUnverified: %v", err)
		}
		for _, r := range seen {
			if r == "blob-1" {
				t.Errorf("blob-1 still reported as unverified before %v", verifiedAt.Add(-time.Minute))
			}
		}
	})

	t.Run("MissingBlobIsNoOp", func(t *testing.T) {
		idx := f.New(t)
		if err := idx.MarkVerified("nonexistent", time.Now()); err != nil {
			t.Errorf("missing blob must be no-op, got %v", err)
		}
	})
}

// --- DeletePacked ---

func runDeletePacked(t *testing.T, f Factory) {
	t.Run("RemovesAllEntriesForOnePack", func(t *testing.T) {
		// Stage two packs with their entries; DeletePacked of
		// pack-1 must clear pack-1's entries while pack-2's
		// stay reachable through LookupPacked.
		idx := f.New(t)

		pack1 := domain.Manifest{
			ArtifactID:   "pack-1",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('1'),
			BlobRef:      "pack-blob-1",
			OriginalSize: 4096,
			CreatedAt:    time.Now(),
		}
		if err := idx.IndexManifest(pack1, manifestfx.PhysAddr("packs/p1"), nil, []domain.PackedEntry{
			{ArtifactID: "a1", BlobRef: "b1", BlobSize: 100, ContentHash: manifestfx.SyntheticHash('a'), PipelineParams: []byte{}},
			{ArtifactID: "a2", BlobRef: "b2", BlobSize: 200, ContentHash: manifestfx.SyntheticHash('b'), PipelineParams: []byte{}},
		}); err != nil {
			t.Fatalf("setup pack-1: %v", err)
		}

		pack2 := domain.Manifest{
			ArtifactID:   "pack-2",
			Type:         domain.ManifestTypePack,
			ContentHash:  manifestfx.SyntheticHash('2'),
			BlobRef:      "pack-blob-2",
			OriginalSize: 4096,
			CreatedAt:    time.Now(),
		}
		if err := idx.IndexManifest(pack2, manifestfx.PhysAddr("packs/p2"), nil, []domain.PackedEntry{
			{ArtifactID: "c1", BlobRef: "d1", BlobSize: 300, ContentHash: manifestfx.SyntheticHash('c'), PipelineParams: []byte{}},
		}); err != nil {
			t.Fatalf("setup pack-2: %v", err)
		}

		if err := idx.DeletePacked("pack-blob-1"); err != nil {
			t.Fatalf("DeletePacked: %v", err)
		}

		// pack-1 entries gone.
		for _, id := range []domain.ArtifactID{"a1", "a2"} {
			_, ok, err := idx.LookupPacked(id)
			if err != nil {
				t.Fatalf("LookupPacked(%s): %v", id, err)
			}
			if ok {
				t.Errorf("LookupPacked(%s) still finds entry after DeletePacked", id)
			}
		}
		// pack-2 entry still there.
		_, ok, err := idx.LookupPacked("c1")
		if err != nil {
			t.Fatalf("LookupPacked(c1): %v", err)
		}
		if !ok {
			t.Error("pack-2 entry c1 must survive DeletePacked(pack-1)")
		}
	})

	t.Run("Idempotent", func(t *testing.T) {
		idx := f.New(t)
		if err := idx.DeletePacked("nonexistent-pack"); err != nil {
			t.Errorf("DeletePacked of unknown pack must be no-op, got %v", err)
		}
	})
}

// --- ListByNamespace ---

func runListByNamespace(t *testing.T, f Factory) {
	// All staging here goes through IndexManifest with distinct
	// (contentHash, blobRef) per artifact, so the
	// (content_hash, original_size) UNIQUE constraint is never
	// touched. The listing tests only care about manifests-side
	// behaviour, but a correct implementation must allow this
	// staging shape.

	t.Run("ExactMatch", func(t *testing.T) {
		idx := f.New(t)
		stage := []struct {
			id, ref, ns string
			fillChar    byte
		}{
			{"a1", "blob-a1", "alpha", 'a'},
			{"a2", "blob-a2", "alpha", 'b'},
			{"b1", "blob-b1", "beta", 'c'},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			m.Namespace = s.ns
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
		}

		got := collectByNamespace(t, idx, "alpha")
		if len(got) != 2 {
			t.Fatalf("got %d manifests, want 2", len(got))
		}
		for _, m := range got {
			if m.Namespace != "alpha" {
				t.Errorf("namespace leak: got %q", m.Namespace)
			}
		}
	})

	t.Run("DefaultNamespace", func(t *testing.T) {
		// Empty-string namespace is the "default" bucket; passing
		// "" to ListByNamespace returns ONLY this bucket, not all
		// namespaces (that's what "*" is for).
		idx := f.New(t)
		mDefault := manifestfx.BlobWithHash("no-ns-1", "blob-d", manifestfx.SyntheticHash('a'), 1024)
		mDefault.Namespace = ""
		if err := idx.IndexManifest(mDefault, manifestfx.PhysAddr("p/d"), nil, nil); err != nil {
			t.Fatal(err)
		}
		mAlpha := manifestfx.BlobWithHash("user-ns", "blob-a", manifestfx.SyntheticHash('b'), 1024)
		mAlpha.Namespace = "alpha"
		if err := idx.IndexManifest(mAlpha, manifestfx.PhysAddr("p/a"), nil, nil); err != nil {
			t.Fatal(err)
		}

		got := collectByNamespace(t, idx, "")
		if len(got) != 1 {
			t.Fatalf("got %d, want 1 (default namespace only)", len(got))
		}
		if got[0].ArtifactID != "no-ns-1" {
			t.Errorf("got %q, want no-ns-1", got[0].ArtifactID)
		}
	})

	t.Run("Wildcard_ExcludesSystem", func(t *testing.T) {
		// "*" is the user-namespace wildcard: everything except
		// the reserved "system." prefix.
		idx := f.New(t)
		stage := []struct {
			id, ref, ns string
			fillChar    byte
		}{
			{"u1", "blob-u1", "alpha", 'a'},
			{"u2", "blob-u2", "beta", 'b'},
			{"s1", "blob-s1", "system.config", 'c'},
			{"s2", "blob-s2", "system.state", 'd'},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			m.Namespace = s.ns
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
		}

		got := collectByNamespace(t, idx, "*")
		if len(got) != 2 {
			t.Fatalf("got %d, want 2 (system.* excluded)", len(got))
		}
		for _, m := range got {
			if strings.HasPrefix(m.Namespace, "system.") {
				t.Errorf("system.* leaked: %s", m.Namespace)
			}
		}
	})

	t.Run("OrderByCreatedAt", func(t *testing.T) {
		// Inserting in reverse temporal order; the iterator must
		// return them sorted ascending by CreatedAt.
		idx := f.New(t)
		now := time.Now().Truncate(time.Second)
		insert := func(id string, ref string, fillChar byte, at time.Time) {
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			m.Namespace = "ns"
			m.CreatedAt = at
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
		insert("third", "blob-t", 'a', now.Add(2*time.Second))
		insert("first", "blob-f", 'b', now)
		insert("second", "blob-s", 'c', now.Add(time.Second))

		got := collectByNamespace(t, idx, "ns")
		want := []domain.ArtifactID{"first", "second", "third"}
		if len(got) != len(want) {
			t.Fatalf("got %d, want %d", len(got), len(want))
		}
		for i, m := range got {
			if m.ArtifactID != want[i] {
				t.Errorf("position %d: got %q, want %q", i, m.ArtifactID, want[i])
			}
		}
	})

	t.Run("StopWalk", func(t *testing.T) {
		idx := f.New(t)
		for i := 0; i < 5; i++ {
			fillChar := byte('a' + i)
			id := "art-" + string(fillChar)
			ref := "blob-" + string(fillChar)
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			m.Namespace = "ns"
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}

		var seen int
		err := idx.ListByNamespace(context.Background(), "ns", func(m domain.Manifest) error {
			seen++
			if seen == 2 {
				return errs.ErrStopWalk
			}
			return nil
		})
		if err != nil {
			t.Fatalf("ErrStopWalk must be swallowed by the iterator, got %v", err)
		}
		if seen != 2 {
			t.Fatalf("expected to stop at 2, saw %d", seen)
		}
	})

	t.Run("CallbackErrorPropagates", func(t *testing.T) {
		idx := f.New(t)
		m := manifestfx.BlobWithHash("a1", "blob-a1", manifestfx.SyntheticHash('a'), 1024)
		m.Namespace = "ns"
		if err := idx.IndexManifest(m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}

		sentinel := errors.New("custom callback error")
		err := idx.ListByNamespace(context.Background(), "ns", func(m domain.Manifest) error {
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel propagated, got %v", err)
		}
	})

	t.Run("PackManifestsExcluded", func(t *testing.T) {
		// Pack manifests live in the index, but listings are for
		// user-visible artifacts only. ListByNamespace must skip
		// pack manifests.
		idx := f.New(t)
		blob := manifestfx.BlobWithHash("blob-1", "ref-blob-1", manifestfx.SyntheticHash('a'), 1024)
		blob.Namespace = "ns"
		if err := idx.IndexManifest(blob, manifestfx.PhysAddr("p/blob"), nil, nil); err != nil {
			t.Fatal(err)
		}

		pack := domain.Manifest{
			ArtifactID:   "pack-1",
			Type:         domain.ManifestTypePack,
			Namespace:    "ns",
			ContentHash:  manifestfx.SyntheticHash('p'),
			BlobRef:      "pack-blob-1",
			OriginalSize: 4096,
			CreatedAt:    time.Now(),
		}
		if err := idx.IndexManifest(pack, manifestfx.PhysAddr("p/pack"), nil, []domain.PackedEntry{
			{ArtifactID: "inner-1", BlobRef: "inner-blob-1", BlobSize: 100,
				ContentHash: manifestfx.SyntheticHash('i'), PipelineParams: []byte{}},
		}); err != nil {
			t.Fatalf("seed pack: %v", err)
		}

		got := collectByNamespace(t, idx, "ns")
		if len(got) != 1 {
			t.Fatalf("got %d, want 1 (pack excluded)", len(got))
		}
		if got[0].Type != domain.ManifestTypeBlob {
			t.Errorf("type: got %q, want blob", got[0].Type)
		}
	})

	t.Run("EmptyResult", func(t *testing.T) {
		idx := f.New(t)
		got := collectByNamespace(t, idx, "nonexistent-ns")
		if len(got) != 0 {
			t.Fatalf("got %d, want 0", len(got))
		}
	})

	t.Run("FieldsRoundTrip", func(t *testing.T) {
		// Every persisted field must round-trip through the
		// iterator. Non-persisted fields (Pipeline, LayoutHeader,
		// Metadata) reach the iterator zero-valued — callers
		// reconstruct them from the manifest file on disk.
		idx := f.New(t)
		now := time.Now().UTC().Truncate(time.Second)
		retention := now.Add(time.Hour)
		src := manifestfx.BlobWithHash("art-1", "blob-1", manifestfx.SyntheticHash('a'), 1024)
		src.Namespace = "ns"
		src.SessionID = "sess-42"
		src.CreatedAt = now
		src.RetentionUntil = retention
		if err := idx.IndexManifest(src, manifestfx.PhysAddr("p"), nil, nil); err != nil {
			t.Fatal(err)
		}

		got := collectByNamespace(t, idx, "ns")
		if len(got) != 1 {
			t.Fatalf("got %d, want 1", len(got))
		}
		m := got[0]
		if m.ArtifactID != src.ArtifactID {
			t.Errorf("ArtifactID: got %q, want %q", m.ArtifactID, src.ArtifactID)
		}
		if m.Type != src.Type {
			t.Errorf("Type: got %q, want %q", m.Type, src.Type)
		}
		if m.Namespace != src.Namespace {
			t.Errorf("Namespace: got %q, want %q", m.Namespace, src.Namespace)
		}
		if m.SessionID != src.SessionID {
			t.Errorf("SessionID: got %q, want %q", m.SessionID, src.SessionID)
		}
		if m.BlobRef != src.BlobRef {
			t.Errorf("BlobRef: got %q, want %q", m.BlobRef, src.BlobRef)
		}
		if !m.CreatedAt.Equal(src.CreatedAt) {
			t.Errorf("CreatedAt: got %v, want %v", m.CreatedAt, src.CreatedAt)
		}
		if !m.RetentionUntil.Equal(src.RetentionUntil) {
			t.Errorf("RetentionUntil: got %v, want %v", m.RetentionUntil, src.RetentionUntil)
		}
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		// A pre-cancelled ctx must surface context.Canceled.
		// Implementations may observe the cancellation either
		// before the query starts or before the first row is
		// scanned — both shapes pass.
		idx := f.New(t)
		for i := 0; i < 3; i++ {
			fillChar := byte('a' + i)
			id := "art-" + string(fillChar)
			ref := "blob-" + string(fillChar)
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			m.Namespace = "ns"
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := idx.ListByNamespace(ctx, "ns", func(m domain.Manifest) error {
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

// collectByNamespace is a small helper that turns a streaming
// ListByNamespace into a slice for table-style assertions.
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

// --- GetBySession ---

func runGetBySession(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		idx := f.New(t)
		stage := []struct {
			id, ref, sess string
			fillChar      byte
		}{
			{"a1", "blob-a1", "sess-1", 'a'},
			{"a2", "blob-a2", "sess-1", 'b'},
			{"b1", "blob-b1", "sess-2", 'c'},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			m.Namespace = "ns"
			m.SessionID = s.sess
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
		}

		ids, err := idx.GetBySession("sess-1")
		if err != nil {
			t.Fatalf("GetBySession: %v", err)
		}
		if len(ids) != 2 {
			t.Fatalf("got %d, want 2", len(ids))
		}
		seen := make(map[domain.ArtifactID]bool)
		for _, id := range ids {
			seen[id] = true
		}
		if !seen["a1"] || !seen["a2"] {
			t.Errorf("missing expected ids: got %v", ids)
		}
	})

	t.Run("Miss", func(t *testing.T) {
		idx := f.New(t)
		ids, err := idx.GetBySession("nonexistent")
		if err != nil {
			t.Fatalf("GetBySession: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("got %d, want 0", len(ids))
		}
	})
}

// --- ListOrphanBlobs ---

func runListOrphanBlobs(t *testing.T, f Factory) {
	// Reaching ref_count=0 through the public API: IndexManifest
	// then DeleteManifest. The blob row remains as an orphan —
	// that is the state ListOrphanBlobs reports.

	t.Run("Basic", func(t *testing.T) {
		idx := f.New(t)
		// Two live blobs, two orphans.
		stage := []struct {
			id, ref  string
			fillChar byte
			deleted  bool
		}{
			{"live-1", "blob-l1", 'a', false},
			{"orph-1", "blob-o1", 'b', true},
			{"orph-2", "blob-o2", 'c', true},
			{"live-2", "blob-l2", 'd', false},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
			if s.deleted {
				if err := idx.DeleteManifest(domain.ArtifactID(s.id), []string{s.ref}); err != nil {
					t.Fatalf("delete %s: %v", s.id, err)
				}
			}
		}

		var got []string
		err := idx.ListOrphanBlobs(context.Background(), func(ref string) error {
			got = append(got, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListOrphanBlobs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d, want 2", len(got))
		}
		seen := make(map[string]bool)
		for _, ref := range got {
			seen[ref] = true
		}
		if !seen["blob-o1"] || !seen["blob-o2"] {
			t.Errorf("expected both orphans, got %v", got)
		}
	})

	t.Run("StopWalk", func(t *testing.T) {
		idx := f.New(t)
		for i := 0; i < 5; i++ {
			fillChar := byte('a' + i)
			id := "art-" + string(fillChar)
			ref := "blob-" + string(fillChar)
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := idx.DeleteManifest(domain.ArtifactID(id), []string{ref}); err != nil {
				t.Fatal(err)
			}
		}

		var seen int
		err := idx.ListOrphanBlobs(context.Background(), func(ref string) error {
			seen++
			if seen == 2 {
				return errs.ErrStopWalk
			}
			return nil
		})
		if err != nil {
			t.Fatalf("ErrStopWalk must be swallowed, got %v", err)
		}
		if seen != 2 {
			t.Fatalf("expected stop at 2, saw %d", seen)
		}
	})
}

// --- ListUnverified ---

func runListUnverified(t *testing.T, f Factory) {
	// IndexManifest creates blobs with no verification timestamp;
	// MarkVerified sets it. The iterator surfaces blobs whose
	// last verification (or absence thereof) places them before
	// the cutoff — never-verified rows always qualify, recently
	// verified rows are skipped.

	t.Run("CutoffBoundary", func(t *testing.T) {
		idx := f.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		stage := []struct {
			id, ref      string
			fillChar     byte
			verifiedAgo  time.Duration
			everVerified bool
		}{
			{"never", "blob-n", 'a', 0, false},
			{"stale", "blob-s", 'b', 10 * time.Minute, true},
			{"fresh", "blob-f", 'c', time.Minute, true},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
			if s.everVerified {
				if err := idx.MarkVerified(s.ref, now.Add(-s.verifiedAgo)); err != nil {
					t.Fatalf("MarkVerified %s: %v", s.ref, err)
				}
			}
		}

		cutoff := now.Add(-5 * time.Minute)
		var got []string
		err := idx.ListUnverified(context.Background(), cutoff, func(ref string) error {
			got = append(got, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListUnverified: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d, want 2 (never+stale)", len(got))
		}
		seen := make(map[string]bool)
		for _, ref := range got {
			seen[ref] = true
		}
		if !seen["blob-n"] {
			t.Error("expected never-verified blob in result")
		}
		if !seen["blob-s"] {
			t.Error("expected stale blob in result")
		}
		if seen["blob-f"] {
			t.Error("fresh blob leaked through cutoff")
		}
	})

	t.Run("OldestFirst", func(t *testing.T) {
		// Sorting order: oldest verification first. NEVER-verified
		// rows are also reported, but the relative position of
		// NEVER vs verified rows is implementation-defined when
		// last_verified_at is treated as a NULL/sentinel value;
		// this test pins down the pure-time ordering between
		// rows that have a verification timestamp.
		idx := f.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		stage := []struct {
			id, ref     string
			fillChar    byte
			verifiedAgo time.Duration
		}{
			{"older", "blob-o", 'a', 3 * time.Hour},
			{"middle", "blob-m", 'b', 2 * time.Hour},
			{"newer", "blob-n", 'c', time.Hour},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			if err := idx.IndexManifest(m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
			if err := idx.MarkVerified(s.ref, now.Add(-s.verifiedAgo)); err != nil {
				t.Fatalf("MarkVerified %s: %v", s.ref, err)
			}
		}

		cutoff := now
		var got []string
		err := idx.ListUnverified(context.Background(), cutoff, func(ref string) error {
			got = append(got, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListUnverified: %v", err)
		}
		want := []string{"blob-o", "blob-m", "blob-n"}
		if len(got) != len(want) {
			t.Fatalf("got %d, want %d", len(got), len(want))
		}
		for i, ref := range got {
			if ref != want[i] {
				t.Errorf("position %d: got %q, want %q", i, ref, want[i])
			}
		}
	})
}

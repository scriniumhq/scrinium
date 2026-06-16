package fspath_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/engine/customindex"
	"scrinium.dev/engine/index/sqlite"
	fsExt "scrinium.dev/x/fspath"
)

// --- helpers ---

// newRegisteredFSPathIndex returns an in-memory sqlite Index plus a
// freshly-registered fspathindex.customindex. Cleanup closes both.
func newRegisteredFSPathIndex(t *testing.T) (*sqlite.Index, *fsExt.CustomIndex) {
	t.Helper()
	idx, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	ci := fsExt.NewIndex()
	if err := idx.CustomIndexes().Register(context.Background(), ci); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return idx, ci
}

// makeFSManifest returns a Manifest with vfsmeta-shaped Ext
// at the given path, plus a mode for diversity.
func makeFSManifest(t *testing.T, id domain.ArtifactID, path string) domain.Manifest {
	t.Helper()
	raw, err := vfsmeta.Encode(vfsmeta.FileSystem{
		Path: path,
		Mode: 0o644,
	})
	if err != nil {
		t.Fatalf("fsmeta.Encode: %v", err)
	}
	return domain.Manifest{
		ArtifactID:   id,
		Namespace:    "files",
		BlobRefs:     []domain.BlobRef{"sha256-" + domain.BlobRef(id)},
		ContentHash:  "sha256-" + domain.ContentHash(id),
		OriginalSize: 100,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          raw,
	}
}

// makeForeignManifest returns a Manifest with metadata that does
// NOT use the vfsmeta schema. fspathindex must skip it.
func makeForeignManifest(t *testing.T, id domain.ArtifactID) domain.Manifest {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"kind": "email/v1", "subject": "hi"})
	return domain.Manifest{
		ArtifactID:   id,
		Namespace:    "mail",
		BlobRefs:     []domain.BlobRef{"sha256-" + domain.BlobRef(id)},
		ContentHash:  "sha256-" + domain.ContentHash(id),
		OriginalSize: 50,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          raw,
	}
}

func physAddr() domain.PhysicalAddress {
	return domain.PhysicalAddress{
		Path: "/blobs/x",
	}
}

// --- registration ---

func TestRegister_Succeeds(t *testing.T) {
	idx, ext := newRegisteredFSPathIndex(t)
	if ext == nil {
		t.Fatal("nil custom index")
	}
	if idx == nil {
		t.Fatal("nil index")
	}
}

func TestRegister_DoubleRegister_Rejects(t *testing.T) {
	idx, _ := newRegisteredFSPathIndex(t)
	ci := idx.CustomIndexes().Register(context.Background(), fsExt.NewIndex())
	if ci == nil {
		t.Error("expected error on second register, got nil")
	}
}

// --- ManifestIndexed handler.go ---

func TestApply_Indexed_VFSMetadata_Stored(t *testing.T) {
	ctx := t.Context()
	idx, ext := newRegisteredFSPathIndex(t)

	m := makeFSManifest(t, "art-1", "photos/2024/sunset.jpg")
	if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}

	raw, ok, err := ext.GetByID("art-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !ok {
		t.Fatal("GetByID: ok=false; expected to find indexed manifest")
	}
	fs, ok, err := vfsmeta.Decode(raw)
	if err != nil || !ok {
		t.Fatalf("Decode persisted bytes: ok=%v err=%v", ok, err)
	}
	if fs.Path != "photos/2024/sunset.jpg" {
		t.Errorf("Path = %q, want photos/2024/sunset.jpg", fs.Path)
	}
}

func TestApply_Indexed_ForeignSchema_Skipped(t *testing.T) {
	ctx := t.Context()
	idx, ext := newRegisteredFSPathIndex(t)

	m := makeForeignManifest(t, "email-1")
	if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}

	_, ok, err := ext.GetByID("email-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if ok {
		t.Error("foreign-schema artifact was indexed by fspathindex; expected skip")
	}
}

func TestApply_Indexed_NoMetadata_Skipped(t *testing.T) {
	ctx := t.Context()
	idx, ext := newRegisteredFSPathIndex(t)

	m := domain.Manifest{
		ArtifactID:   "bare-1",
		Namespace:    "files",
		BlobRefs:     []domain.BlobRef{"sha256-bare"},
		ContentHash:  "sha256-bare",
		OriginalSize: 10,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		// Ext is nil
	}
	if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	_, ok, _ := ext.GetByID("bare-1")
	if ok {
		t.Error("manifest with nil Ext indexed; expected skip")
	}
}

// --- LookupByPath ---

func TestLookupByPath_Hit(t *testing.T) {
	ctx := t.Context()
	idx, ext := newRegisteredFSPathIndex(t)

	m := makeFSManifest(t, "art-photo", "photos/2024/sunset.jpg")
	if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}

	id, ok, err := ext.LookupByPath("photos/2024/sunset.jpg")
	if err != nil {
		t.Fatalf("LookupByPath: %v", err)
	}
	if !ok {
		t.Fatal("LookupByPath: ok=false")
	}
	if id != "art-photo" {
		t.Errorf("LookupByPath returned %q, want art-photo", id)
	}
}

func TestLookupByPath_Miss(t *testing.T) {
	_, ext := newRegisteredFSPathIndex(t)
	_, ok, err := ext.LookupByPath("nonexistent/path.txt")
	if err != nil {
		t.Fatalf("LookupByPath: %v", err)
	}
	if ok {
		t.Error("LookupByPath returned ok=true on missing path")
	}
}

// --- WalkAll ---

func TestWalkAll_VisitsAll(t *testing.T) {
	ctx := t.Context()
	idx, ext := newRegisteredFSPathIndex(t)

	pairs := map[domain.ArtifactID]string{
		"a-1": "alpha/file1",
		"a-2": "alpha/file2",
		"b-1": "beta/file1",
	}
	for id, path := range pairs {
		m := makeFSManifest(t, id, path)
		if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
			t.Fatalf("IndexManifest %q: %v", id, err)
		}
	}

	visited := map[domain.ArtifactID]string{}
	err := ext.WalkAll(func(id domain.ArtifactID, raw json.RawMessage) error {
		fs, _, err := vfsmeta.Decode(raw)
		if err != nil {
			return err
		}
		visited[id] = fs.Path
		return nil
	})
	if err != nil {
		t.Fatalf("WalkAll: %v", err)
	}
	if len(visited) != 3 {
		t.Errorf("WalkAll visited %d, want 3: %v", len(visited), visited)
	}
	for id, want := range pairs {
		if got := visited[id]; got != want {
			t.Errorf("WalkAll: %q got %q, want %q", id, got, want)
		}
	}
}

// --- ManifestDeleted handler.go ---

func TestApply_Deleted_RemovesEntries(t *testing.T) {
	ctx := t.Context()
	idx, ext := newRegisteredFSPathIndex(t)

	m := makeFSManifest(t, "art-del", "tmp/file.txt")
	if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}

	// Confirm presence.
	_, ok, _ := ext.GetByID("art-del")
	if !ok {
		t.Fatal("pre-delete: not indexed")
	}

	// Delete via the index. It will dispatch ManifestDeleted.
	if err := idx.DeleteManifest(ctx, m.Digest); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	_, ok, _ = ext.GetByID("art-del")
	if ok {
		t.Error("post-delete: still in byID")
	}
	_, ok, _ = ext.LookupByPath("tmp/file.txt")
	if ok {
		t.Error("post-delete: still findable by path")
	}
}

func TestApply_Deleted_NotIndexed_NoOp(t *testing.T) {
	ctx := t.Context()
	idx, _ := newRegisteredFSPathIndex(t)

	// Index a non-vfsmeta manifest then delete; fspathindex should
	// silently no-op since we never indexed it.
	m := makeForeignManifest(t, "email-2")
	if err := idx.IndexManifest(ctx, m, physAddr()); err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	if err := idx.DeleteManifest(ctx, m.Digest); err != nil {
		t.Errorf("DeleteManifest of un-indexed artifact failed: %v", err)
	}
}

// --- Strict consistency: Apply error rolls back the main write ---

// applyError makes the fspathindex fail on a specific artifact id by
// passing in a malformed metadata payload that decodes as vfsmeta
// (right marker) but has an invalid type for Path.
func TestApply_BrokenVFSMeta_RollsBackMainWrite(t *testing.T) {
	ctx := t.Context()
	idx, _ := newRegisteredFSPathIndex(t)

	// Construct a payload with the right marker but wrong type
	// for Path: Decode will return an error.
	bad := json.RawMessage(`{"kind":"scrinium.fs/v1","path":12345}`)
	m := domain.Manifest{
		ArtifactID:   "art-bad",
		Namespace:    "files",
		BlobRefs:     []domain.BlobRef{"sha256-bad"},
		ContentHash:  "sha256-bad",
		OriginalSize: 10,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          bad,
	}
	err := idx.IndexManifest(ctx, m, physAddr())
	if err == nil {
		t.Fatal("expected error from broken fsmeta, got nil")
	}

	// Main write must have rolled back too.
	_, exists, err := idx.ResolveManifestDigest(ctx, "art-bad")
	if err != nil {
		t.Fatalf("ResolveManifestDigest: %v", err)
	}
	if exists {
		t.Error("manifest committed despite fspathindex failure (atomicity broken)")
	}
}

// --- Read API on un-registered custom index ---

func TestReadAPI_NotRegistered_Errs(t *testing.T) {
	ci := fsExt.NewIndex()
	_, _, err := ci.GetByID("anything")
	if err == nil {
		t.Error("GetByID on un-registered custom index returned nil; want error")
	}
	_, _, err = ci.LookupByPath("anything")
	if err == nil {
		t.Error("LookupByPath on un-registered custom index returned nil; want error")
	}
	err = ci.WalkAll(func(domain.ArtifactID, json.RawMessage) error { return nil })
	if err == nil {
		t.Error("WalkAll on un-registered custom index returned nil; want error")
	}
}

// --- Schema regression rejection at backend level ---

// We can't easily exercise this at the projection/fspathindex level
// because schemaVersion is a package-private constant. The test
// in index/sqlite/extension_test.go (TestRegister_SchemaRegression)
// covers the mechanism generally.

// --- Subscribe matrix sanity ---

func TestSubscribe_OnlyManifestEvents(t *testing.T) {
	ci := fsExt.NewIndex()
	subs := ci.Subscribe()
	if len(subs) != 2 {
		t.Fatalf("Subscribe: got %d kinds, want 2", len(subs))
	}
	have := map[customindex.EventKind]bool{}
	for _, k := range subs {
		have[k] = true
	}
	if !have[customindex.EventKindManifestIndexed] {
		t.Error("missing EventKindManifestIndexed")
	}
	if !have[customindex.EventKindManifestDeleted] {
		t.Error("missing EventKindManifestDeleted")
	}
}

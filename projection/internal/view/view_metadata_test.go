package view_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
)

// --- helpers ---

// countingMetadataSource records every call to Ext so tests can
// assert the fast-path was actually taken.
type countingMetadataSource struct {
	store map[domain.ArtifactID]json.RawMessage
	calls atomic.Int32
}

func newCountingMetadataSource() *countingMetadataSource {
	return &countingMetadataSource{
		store: make(map[domain.ArtifactID]json.RawMessage),
	}
}

func (c *countingMetadataSource) put(id domain.ArtifactID, raw json.RawMessage) {
	c.store[id] = raw
}

func (c *countingMetadataSource) Metadata(id domain.ArtifactID) (json.RawMessage, bool, error) {
	c.calls.Add(1)
	raw, ok := c.store[id]
	return raw, ok, nil
}

// strippedManifest returns a Manifest with Ext cleared,
// simulating what an index-backed Walk produces.
func strippedManifest(id domain.ArtifactID, namespace string) domain.Manifest {
	return domain.Manifest{
		ArtifactID:   id,
		Namespace:    namespace,
		BlobRefs:     []domain.BlobRef{"sha256-" + domain.BlobRef(id)},
		ContentHash:  "sha256-" + domain.ContentHash(id),
		OriginalSize: 100,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		// Ext intentionally nil.
	}
}

func encodeVFSMeta(t *testing.T, path string) json.RawMessage {
	t.Helper()
	raw, err := vfsmeta.Encode(vfsmeta.FileSystem{Path: path, Mode: 0o644})
	if err != nil {
		t.Fatalf("vfsmeta.Encode: %v", err)
	}
	return raw
}

// --- tests ---

// TestBackfill_FastPath_UsesExtSource verifies that when a
// MetadataSource is configured, vw.backfill consults it for
// every walked manifest. Source.Get is still callable (the slow
// path is fallback), but the fast path should hit first when the
// source has the artifact.
func TestBackfill_FastPath_UsesExtSource(t *testing.T) {
	src := projectionfx.New()
	ms := newCountingMetadataSource()

	for i, path := range []string{"a.txt", "b.txt", "c.txt"} {
		id := domain.ArtifactID([]byte{'i', 'd', '0' + byte(i)})
		// Walk-side: stripped (no Ext).
		src.Add(strippedManifest(id, "files"), nil)
		// Fast-path side: full metadata.
		ms.put(id, encodeVFSMeta(t, path))
	}

	v, err := vw.New(context.Background(), src,
		vw.WithPathResolver(vfsmeta.Resolver),
		vw.WithMetadataSource(ms),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	if v == nil {
		t.Fatal("nil view")
	}

	// Every walked manifest should have triggered exactly one
	// MetadataSource lookup.
	if got := ms.calls.Load(); got != 3 {
		t.Errorf("MetadataSource.Ext called %d times, want 3", got)
	}
}

// TestBackfill_FastPath_FallsBackOnMiss verifies that when the
// MetadataSource doesn't have a record (e.g. artifact written
// before the custom index was registered), backfill silently falls
// back to Source.Get and the View still ends up with the
// ext block for that manifest.
func TestBackfill_FastPath_FallsBackOnMiss(t *testing.T) {
	src := projectionfx.New()
	ms := newCountingMetadataSource()

	// One artifact in MetadataSource with full ext payload.
	idHit := domain.ArtifactID("hit")
	src.Add(domain.Manifest{
		ArtifactID:   idHit,
		Namespace:    "files",
		BlobRefs:     []domain.BlobRef{"sha256-hit"},
		OriginalSize: 1,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          encodeVFSMeta(t, "in-source.txt"),
	}, nil)
	ms.put(idHit, encodeVFSMeta(t, "in-source.txt"))

	// Another artifact NOT in MetadataSource. FakeSource keeps the
	// full manifest in-memory; the slow-path Get returns it,
	// recovering Ext for the vw.
	idMiss := domain.ArtifactID("miss")
	src.Add(domain.Manifest{
		ArtifactID:   idMiss,
		Namespace:    "files",
		BlobRefs:     []domain.BlobRef{"sha256-miss"},
		OriginalSize: 1,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          encodeVFSMeta(t, "fallback.txt"),
	}, nil)
	// Intentionally NOT calling ms.put for idMiss.

	v, err := vw.New(context.Background(), src,
		vw.WithPathResolver(vfsmeta.Resolver),
		vw.WithMetadataSource(ms),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}

	// Both artifacts should be findable by their resolved paths.
	if _, err := v.GetIn(vw.RootByPath, "in-source.txt"); err != nil {
		t.Errorf("fast-path artifact not in View by path: %v", err)
	}
	if _, err := v.GetIn(vw.RootByPath, "fallback.txt"); err != nil {
		t.Errorf("fallback (Source.Get) artifact not in View by path: %v", err)
	}

	// Fast path was tried for both.
	if got := ms.calls.Load(); got != 2 {
		t.Errorf("MetadataSource.Ext called %d times, want 2", got)
	}
}

// TestBackfill_NoExtSource_FallsBackToGet verifies the
// backwards-compatible slow path: with no MetadataSource
// configured, View round-trips Source.Get for each manifest. We
// detect this by injecting a Get error and observing that the
// resolver doesn't see Ext (path resolution fails, but the
// artifact still ends up indexed by id in by-artifact).
func TestBackfill_NoExtSource_FallsBackToGet(t *testing.T) {
	src := projectionfx.New()

	// Strip Ext from the Walk-side manifest so the resolver
	// can't produce a path without Get's help.
	id := domain.ArtifactID("only-walk")
	src.Add(strippedManifest(id, "files"), nil)

	// Inject a Get error so the slow path also fails. The View
	// should still build (errors swallowed) but the artifact has
	// no path.
	src.SetGetErr(errors.New("get unavailable"))

	v, err := vw.New(context.Background(), src,
		vw.WithPathResolver(vfsmeta.Resolver),
		// No WithMetadataSource here.
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}

	// Path resolution failed (no Ext reached the resolver), so
	// the artifact must be absent from by-path.
	if _, err := v.GetIn(vw.RootByPath, "only-walk"); err == nil {
		t.Error("artifact unexpectedly indexed under by-path")
	}

	// But it's still indexed by id under by-artifact (id-based
	// path needs no resolver). This proves backfill completed
	// for the artifact even though metadata recovery failed —
	// "errors swallowed, build keeps going" semantics.
	if _, err := v.GetIn(vw.RootByArtifact, byArtifactPathForTest(id)); err != nil {
		t.Errorf("artifact not in by-artifact tree: %v", err)
	}
}

// byArtifactPathForTest mirrors the in-package byArtifactPath
// helper. Kept here so the external test stays self-contained.
// Algorithm: take the part of id after the first '-' (or the
// whole id if no '-'), and lay out as hh/hh/<id>.
func byArtifactPathForTest(id domain.ArtifactID) string {
	s := string(id)
	if i := indexByte(s, '-'); i >= 0 {
		s = s[i+1:]
	} else {
		s = string(id)
	}
	if len(s) < 4 {
		return "_short/" + string(id)
	}
	return s[:2] + "/" + s[2:4] + "/" + string(id)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// TestWithFSPathIndex_Convenience verifies WithFSPathIndex is just a
// pass-through for WithMetadataSource. We confirm by passing a
// counting source through WithFSPathIndex and observing the calls.
func TestWithFSPathIndex_Convenience(t *testing.T) {
	src := projectionfx.New()
	ms := newCountingMetadataSource()

	id := domain.ArtifactID("a")
	src.Add(strippedManifest(id, "files"), nil)
	ms.put(id, encodeVFSMeta(t, "fs.txt"))

	_, err := vw.New(context.Background(), src,
		vw.WithPathResolver(vfsmeta.Resolver),
		vw.WithFSPathIndex(ms),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}

	if got := ms.calls.Load(); got != 1 {
		t.Errorf("MetadataSource.Ext called %d times, want 1", got)
	}
}

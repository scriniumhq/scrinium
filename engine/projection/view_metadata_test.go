package projection_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/projection/fsmeta"
	"scrinium.dev/internal/testutil/projectionfx"
)

// --- helpers ---

// countingExtSource records every call to Ext so tests can
// assert the fast-path was actually taken.
type countingExtSource struct {
	store map[domain.ArtifactID]json.RawMessage
	calls atomic.Int32
}

func newCountingExtSource() *countingExtSource {
	return &countingExtSource{
		store: make(map[domain.ArtifactID]json.RawMessage),
	}
}

func (c *countingExtSource) put(id domain.ArtifactID, raw json.RawMessage) {
	c.store[id] = raw
}

func (c *countingExtSource) Ext(id domain.ArtifactID) (json.RawMessage, bool, error) {
	c.calls.Add(1)
	raw, ok := c.store[id]
	return raw, ok, nil
}

// strippedManifest returns a Manifest with Ext cleared,
// simulating what an index-backed Walk produces.
func strippedManifest(id domain.ArtifactID, namespace string) domain.Manifest {
	return domain.Manifest{
		ArtifactID:   id,
		Type:         domain.ManifestTypeBlob,
		Namespace:    namespace,
		BlobRef:      "sha256-" + domain.BlobRef(id),
		ContentHash:  "sha256-" + domain.ContentHash(id),
		OriginalSize: 100,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		// Ext intentionally nil.
	}
}

func encodeFSMeta(t *testing.T, path string) json.RawMessage {
	t.Helper()
	raw, err := fsmeta.Encode(fsmeta.FileSystem{Path: path, Mode: 0o644})
	if err != nil {
		t.Fatalf("fsmeta.Encode: %v", err)
	}
	return raw
}

// --- tests ---

// TestBackfill_FastPath_UsesExtSource verifies that when a
// ExtSource is configured, View.backfill consults it for
// every walked manifest. Source.Get is still callable (the slow
// path is fallback), but the fast path should hit first when the
// source has the artifact.
func TestBackfill_FastPath_UsesExtSource(t *testing.T) {
	src := projectionfx.New()
	ms := newCountingExtSource()

	for i, path := range []string{"a.txt", "b.txt", "c.txt"} {
		id := domain.ArtifactID([]byte{'i', 'd', '0' + byte(i)})
		// Walk-side: stripped (no Ext).
		src.Add(strippedManifest(id, "files"), nil)
		// Fast-path side: full metadata.
		ms.put(id, encodeFSMeta(t, path))
	}

	v, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithExtSource(ms),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	if v == nil {
		t.Fatal("nil view")
	}

	// Every walked manifest should have triggered exactly one
	// ExtSource lookup.
	if got := ms.calls.Load(); got != 3 {
		t.Errorf("ExtSource.Ext called %d times, want 3", got)
	}
}

// TestBackfill_FastPath_FallsBackOnMiss verifies that when the
// ExtSource doesn't have a record (e.g. artifact written
// before the extension was registered), backfill silently falls
// back to Source.Get and the View still ends up with the
// ext block for that manifest.
func TestBackfill_FastPath_FallsBackOnMiss(t *testing.T) {
	src := projectionfx.New()
	ms := newCountingExtSource()

	// One artifact in ExtSource with full ext payload.
	idHit := domain.ArtifactID("hit")
	src.Add(domain.Manifest{
		ArtifactID:   idHit,
		Type:         domain.ManifestTypeBlob,
		Namespace:    "files",
		BlobRef:      "sha256-hit",
		OriginalSize: 1,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          encodeFSMeta(t, "in-source.txt"),
	}, nil)
	ms.put(idHit, encodeFSMeta(t, "in-source.txt"))

	// Another artifact NOT in ExtSource. FakeSource keeps the
	// full manifest in-memory; the slow-path Get returns it,
	// recovering Ext for the View.
	idMiss := domain.ArtifactID("miss")
	src.Add(domain.Manifest{
		ArtifactID:   idMiss,
		Type:         domain.ManifestTypeBlob,
		Namespace:    "files",
		BlobRef:      "sha256-miss",
		OriginalSize: 1,
		CreatedAt:    time.Now().UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Ext:          encodeFSMeta(t, "fallback.txt"),
	}, nil)
	// Intentionally NOT calling ms.put for idMiss.

	v, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithExtSource(ms),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}

	// Both artifacts should be findable by their resolved paths.
	if _, err := v.GetByPath("in-source.txt"); err != nil {
		t.Errorf("fast-path artifact not in View by path: %v", err)
	}
	if _, err := v.GetByPath("fallback.txt"); err != nil {
		t.Errorf("fallback (Source.Get) artifact not in View by path: %v", err)
	}

	// Fast path was tried for both.
	if got := ms.calls.Load(); got != 2 {
		t.Errorf("ExtSource.Ext called %d times, want 2", got)
	}
}

// TestBackfill_NoExtSource_FallsBackToGet verifies the
// backwards-compatible slow path: with no ExtSource
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

	v, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
		// No WithExtSource here.
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}

	// Path resolution failed (no Ext reached the resolver), so
	// the artifact must be absent from by-path.
	if _, err := v.GetByPath("only-walk"); err == nil {
		t.Error("artifact unexpectedly indexed under by-path")
	}

	// But it's still indexed by id under by-artifact (id-based
	// path needs no resolver). This proves backfill completed
	// for the artifact even though metadata recovery failed —
	// "errors swallowed, build keeps going" semantics.
	if _, err := v.GetByArtifact(byArtifactPathForTest(id)); err != nil {
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

// TestWithFSIndex_Convenience verifies WithFSIndex is just a
// pass-through for WithExtSource. We confirm by passing a
// counting source through WithFSIndex and observing the calls.
func TestWithFSIndex_Convenience(t *testing.T) {
	src := projectionfx.New()
	ms := newCountingExtSource()

	id := domain.ArtifactID("a")
	src.Add(strippedManifest(id, "files"), nil)
	ms.put(id, encodeFSMeta(t, "fs.txt"))

	_, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFSIndex(ms),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}

	if got := ms.calls.Load(); got != 1 {
		t.Errorf("ExtSource.Ext called %d times, want 1", got)
	}
}

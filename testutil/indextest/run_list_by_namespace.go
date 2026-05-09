package indextest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/testutil/manifestfx"
)

// --- ListByNamespace ---

func runListByNamespace(t *testing.T, f Factory) {
	// All staging here goes through IndexManifest with distinct
	// (contentHash, blobRef) per artifact, so the
	// (content_hash, original_size) UNIQUE constraint is never
	// touched. The listing tests only care about manifests-side
	// behaviour, but a correct implementation must allow this
	// staging shape.

	t.Run("ExactMatch", func(t *testing.T) {
		ctx := t.Context()
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
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
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
		ctx := t.Context()
		// Empty-string namespace is the "default" bucket; passing
		// "" to ListByNamespace returns ONLY this bucket, not all
		// namespaces (that's what "*" is for).
		idx := f.New(t)
		mDefault := manifestfx.BlobWithHash("no-ns-1", "blob-d", manifestfx.SyntheticHash('a'), 1024)
		mDefault.Namespace = ""
		if err := idx.IndexManifest(ctx, mDefault, manifestfx.PhysAddr("p/d"), nil, nil); err != nil {
			t.Fatal(err)
		}
		mAlpha := manifestfx.BlobWithHash("user-ns", "blob-a", manifestfx.SyntheticHash('b'), 1024)
		mAlpha.Namespace = "alpha"
		if err := idx.IndexManifest(ctx, mAlpha, manifestfx.PhysAddr("p/a"), nil, nil); err != nil {
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
		ctx := t.Context()
		// "*" is the user-namespace wildcard: everything except
		// the reserved "system." prefix.
		idx := f.New(t)
		stage := []struct {
			id, ref, ns string
			fillChar    byte
		}{
			{"u1", "blob-u1", "alpha", 'a'},
			{"u2", "blob-u2", "beta", 'b'},
			{"s1", "blob-s1", domain.NamespaceSystemConfig, 'c'},
			{"s2", "blob-s2", domain.NamespaceSystemState, 'd'},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			m.Namespace = s.ns
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
		}

		got := collectByNamespace(t, idx, domain.NamespaceWildcard)
		if len(got) != 2 {
			t.Fatalf("got %d, want 2 (system.* excluded)", len(got))
		}
		for _, m := range got {
			if strings.HasPrefix(m.Namespace, domain.NamespaceSystemPrefix) {
				t.Errorf("system.* leaked: %s", m.Namespace)
			}
		}
	})

	t.Run("OrderByCreatedAt", func(t *testing.T) {
		ctx := t.Context()
		// Inserting in reverse temporal order; the iterator must
		// return them sorted ascending by CreatedAt.
		idx := f.New(t)
		now := time.Now().Truncate(time.Second)
		insert := func(id string, ref string, fillChar byte, at time.Time) {
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			m.Namespace = "ns"
			m.CreatedAt = at
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
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
		ctx := t.Context()
		idx := f.New(t)
		for i := 0; i < 5; i++ {
			fillChar := byte('a' + i)
			id := "art-" + string(fillChar)
			ref := "blob-" + string(fillChar)
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			m.Namespace = "ns"
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
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
		ctx := t.Context()
		idx := f.New(t)
		m := manifestfx.BlobWithHash("a1", "blob-a1", manifestfx.SyntheticHash('a'), 1024)
		m.Namespace = "ns"
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p"), nil, nil); err != nil {
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
		ctx := t.Context()
		// Pack manifests live in the index, but listings are for
		// user-visible artifacts only. ListByNamespace must skip
		// pack manifests.
		idx := f.New(t)
		blob := manifestfx.BlobWithHash("blob-1", "ref-blob-1", manifestfx.SyntheticHash('a'), 1024)
		blob.Namespace = "ns"
		if err := idx.IndexManifest(ctx, blob, manifestfx.PhysAddr("p/blob"), nil, nil); err != nil {
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
		if err := idx.IndexManifest(ctx, pack, manifestfx.PhysAddr("p/pack"), nil, []domain.PackedEntry{
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
		ctx := t.Context()
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
		if err := idx.IndexManifest(ctx, src, manifestfx.PhysAddr("p"), nil, nil); err != nil {
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
		ctx := t.Context()
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
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
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

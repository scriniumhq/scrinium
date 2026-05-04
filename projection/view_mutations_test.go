package projection_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/projection"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// Helpers makeManifestWithMeta and recordingBus live in
// view_collisions_test.go; both files share the projection_test
// package.

// --- by-session ---

func TestBySession_Populated(t *testing.T) {
	src := newFakeSource()
	now := time.Now().UTC()
	src.add(makeManifest("sha256-aaaa1111", "f", "abcd1234", 100, now), nil)
	src.add(makeManifest("sha256-bbbb2222", "f", "abcd1234", 200, now), nil)

	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	// by-session/<aa>/<bb>/<sid>/<aid>: ab/cd/abcd1234/sha256-aaaa1111
	if _, err := v.GetBySession("ab/cd/abcd1234/sha256-aaaa1111"); err != nil {
		t.Errorf("first artifact: %v", err)
	}
	if _, err := v.GetBySession("ab/cd/abcd1234/sha256-bbbb2222"); err != nil {
		t.Errorf("second artifact: %v", err)
	}
	if v.Stats.SessionCount != 1 {
		t.Errorf("SessionCount: got %d, want 1", v.Stats.SessionCount)
	}
}

func TestBySession_EmptySessionSkipped(t *testing.T) {
	// Artifacts without SessionID are not present in by-session.
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	count := 0
	for n, err := range v.WalkBySession("") {
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		if n.FS.Path != "" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("by-session should be empty, got %d nodes", count)
	}
}

func TestBySession_Short(t *testing.T) {
	// SessionID < 4 chars buckets under _short.
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "ab", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	if _, err := v.GetBySession("_short/ab/sha256-aabbccdd"); err != nil {
		t.Errorf("short session bucket: %v", err)
	}
}

// --- by-namespace ---

func TestByNamespace_Populated(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "photos", "s", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	if _, err := v.GetByNamespace("photos/aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-namespace: %v", err)
	}
	if v.Stats.NamespaceCount != 1 {
		t.Errorf("NamespaceCount: got %d, want 1", v.Stats.NamespaceCount)
	}
}

func TestByNamespace_EmptyBucketsAsDefault(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "", "s", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	if _, err := v.GetByNamespace("_default/aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-namespace _default: %v", err)
	}
}

// --- by-date ---

func TestByDate_Populated(t *testing.T) {
	src := newFakeSource()
	t0 := time.Date(2024, 5, 3, 14, 23, 5, 0, time.UTC)
	src.add(makeManifest("sha256-aabbccddeeff0011", "f", "s", 100, t0), nil)

	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	expected := "2024/05/03/14-23-05-aabbccddeeff0011.bin"
	if _, err := v.GetByDate(expected); err != nil {
		t.Errorf("by-date %q: %v", expected, err)
	}
}

// --- ViewRebuilt event ---

func TestEvent_ViewRebuilt(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)

	bus := newRecordingBus()
	v, _ := projection.NewView(context.Background(), src,
		projection.WithEventBus(bus))
	defer v.Close()

	rebuilt := bus.byType(projection.EventViewRebuilt)
	if len(rebuilt) != 1 {
		t.Fatalf("expected 1 ViewRebuilt event, got %d", len(rebuilt))
	}
	p := rebuilt[0].Payload.(projection.ViewRebuiltPayload)
	if p.NodeCount != 1 {
		t.Errorf("NodeCount: got %d, want 1", p.NodeCount)
	}
}

// --- Add ---

func TestAdd_AppearsInAllTrees(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	m := makeManifestWithMeta("sha256-aabbccdd", "files", "sess1",
		"photos/img.jpg", 100, time.Now().UTC())
	if err := v.Add(m); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := v.GetByPath("photos/img.jpg"); err != nil {
		t.Errorf("by-path: %v", err)
	}
	if _, err := v.GetByArtifact("aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-artifact: %v", err)
	}
	if _, err := v.GetBySession("se/ss/sess1/sha256-aabbccdd"); err != nil {
		t.Errorf("by-session: %v", err)
	}
	if _, err := v.GetByNamespace("files/aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-namespace: %v", err)
	}
	if v.Stats.TotalNodes != 1 {
		t.Errorf("TotalNodes: got %d, want 1", v.Stats.TotalNodes)
	}
}

func TestAdd_Idempotent(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	m := makeManifestWithMeta("sha256-aabbccdd", "f", "s",
		"a", 100, time.Now().UTC())
	if err := v.Add(m); err != nil {
		t.Fatal(err)
	}
	if err := v.Add(m); err != nil {
		t.Fatal(err)
	}
	if v.Stats.TotalNodes != 1 {
		t.Errorf("expected 1 after duplicate Add, got %d", v.Stats.TotalNodes)
	}
}

func TestAdd_OnClosedView(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src)
	v.Close()

	err := v.Add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()))
	if !errors.Is(err, errs.ErrViewClosed) {
		t.Errorf("expected ErrViewClosed, got %v", err)
	}
}

// --- Remove ---

func TestRemove_DropsFromAllTrees(t *testing.T) {
	src := newFakeSource()
	src.add(
		makeManifestWithMeta("sha256-aabbccdd", "files", "sess1",
			"photos/img.jpg", 100, time.Now().UTC()),
		nil,
	)
	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if err := v.Remove("sha256-aabbccdd"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := v.GetByPath("photos/img.jpg"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("by-path should be gone, got %v", err)
	}
	if _, err := v.GetByArtifact("aa/bb/sha256-aabbccdd"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("by-artifact should be gone, got %v", err)
	}
	if v.Stats.TotalNodes != 0 {
		t.Errorf("TotalNodes: got %d, want 0", v.Stats.TotalNodes)
	}
}

func TestRemove_PromotesLoserOnOwnerRemove(t *testing.T) {
	src := newFakeSource()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	src.add(makeManifestWithMeta("sha256-aaaa1111", "f", "s",
		"shared", 100, older), nil)
	src.add(makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"shared", 200, newer), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if err := v.Remove("sha256-bbbb2222"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	n, err := v.GetByPath("shared")
	if err != nil {
		t.Fatalf("GetByPath after promote: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-aaaa1111") {
		t.Errorf("expected aaaa1111 promoted; got %q", n.Artifact.ArtifactID)
	}
}

func TestRemove_LoserDoesNotAffectOwner(t *testing.T) {
	src := newFakeSource()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	src.add(makeManifestWithMeta("sha256-aaaa1111", "f", "s",
		"shared", 100, older), nil)
	src.add(makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"shared", 200, newer), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if err := v.Remove("sha256-aaaa1111"); err != nil {
		t.Fatalf("Remove loser: %v", err)
	}
	n, err := v.GetByPath("shared")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("owner changed: got %q", n.Artifact.ArtifactID)
	}
}

func TestRemove_Idempotent(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	if err := v.Remove("nonexistent-id"); err != nil {
		t.Errorf("Remove for unknown id should be no-op, got %v", err)
	}
}

// --- Move ---

func TestMove_RenameFile(t *testing.T) {
	src := newFakeSource()
	src.add(
		makeManifestWithMeta("sha256-aaaa1111", "f", "s",
			"old/path.txt", 100, time.Now().UTC()),
		nil,
	)
	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	newM := makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"new/path.txt", 100, time.Now().UTC())
	if err := v.Move("old/path.txt", "new/path.txt", newM); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if _, err := v.GetByPath("old/path.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("old path should be gone, got %v", err)
	}
	n, err := v.GetByPath("new/path.txt")
	if err != nil {
		t.Fatalf("GetByPath new: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("new artifact: got %q", n.Artifact.ArtifactID)
	}
}

// --- Filter prefix ---

func TestNewView_FilterPrefix(t *testing.T) {
	src := newFakeSource()
	now := time.Now().UTC()
	src.add(makeManifestWithMeta("sha256-aaaa1111", "f", "s",
		"photos/a.jpg", 100, now), nil)
	src.add(makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"docs/b.txt", 200, now), nil)

	v, _ := projection.NewView(
		context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFilter(projection.ViewFilter{Prefix: "photos/"}),
	)
	defer v.Close()

	if v.Stats.TotalNodes != 1 {
		t.Errorf("TotalNodes: got %d, want 1", v.Stats.TotalNodes)
	}
	if _, err := v.GetByPath("photos/a.jpg"); err != nil {
		t.Errorf("photos: %v", err)
	}
	if _, err := v.GetByPath("docs/b.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("docs should be filtered: got %v", err)
	}
}

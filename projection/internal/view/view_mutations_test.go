package view_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"scrinium.dev/event"
	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/manifestfx"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/domain"
	"scrinium.dev/domain/fsmeta"
	"scrinium.dev/errs"
)

// --- by-session ---

func TestBySession_Populated(t *testing.T) {
	src := projectionfx.New()
	now := time.Now().UTC()
	src.Add(makeManifest("sha256-aaaa1111", "f", "abcd1234", 100, now), nil)
	src.Add(makeManifest("sha256-bbbb2222", "f", "abcd1234", 200, now), nil)

	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	if _, err := v.GetIn(vw.RootBySession, "abcd1234/sha256-aaaa1111"); err != nil {
		t.Errorf("first artifact: %v", err)
	}
	if _, err := v.GetIn(vw.RootBySession, "abcd1234/sha256-bbbb2222"); err != nil {
		t.Errorf("second artifact: %v", err)
	}
	if v.Stats.SessionCount != 1 {
		t.Errorf("SessionCount: got %d, want 1", v.Stats.SessionCount)
	}
}

func TestBySession_EmptySessionSkipped(t *testing.T) {
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "f", "", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	count := 0
	for n, err := range v.WalkIn(vw.RootBySession, "") {
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
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "f", "ab", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	if _, err := v.GetIn(vw.RootBySession, "ab/sha256-aabbccdd"); err != nil {
		t.Errorf("short session bucket: %v", err)
	}
}

// --- by-namespace ---

func TestByNamespace_Populated(t *testing.T) {
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "photos", "s", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	if _, err := v.GetIn(vw.RootByNamespace, "photos/aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-namespace: %v", err)
	}
	if v.Stats.NamespaceCount != 1 {
		t.Errorf("NamespaceCount: got %d, want 1", v.Stats.NamespaceCount)
	}
}

func TestByNamespace_EmptyBucketsAsDefault(t *testing.T) {
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "", "s", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	if _, err := v.GetIn(vw.RootByNamespace, "_default/aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-namespace _default: %v", err)
	}
}

// --- by-date ---

func TestByDate_Populated(t *testing.T) {
	src := projectionfx.New()
	t0 := time.Date(2024, 5, 3, 14, 23, 5, 0, time.UTC)
	src.Add(makeManifest("sha256-aabbccddeeff0011", "f", "s", 100, t0), nil)

	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	expected := "2024/05/03/14-23-05-aabbccddeeff0011.bin"
	if _, err := v.GetIn(vw.RootByDate, expected); err != nil {
		t.Errorf("by-date %q: %v", expected, err)
	}
}

// --- ViewRebuilt event ---

func TestEvent_ViewRebuilt(t *testing.T) {
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)

	bus := eventfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithEventBus(bus))
	defer v.Close()

	if got := bus.Count(event.EventViewRebuilt); got != 1 {
		t.Fatalf("expected 1 ViewRebuilt event, got %d", got)
	}
	rebuilt := bus.ByType(event.EventViewRebuilt)
	p := rebuilt[0].Payload.(event.RebuiltPayload)
	if p.NodeCount != 1 {
		t.Errorf("NodeCount: got %d, want 1", p.NodeCount)
	}
}

// --- Add ---

func TestAdd_AppearsInAllTrees(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	m := manifestfx.ManifestWithFsmetaPath("sha256-aabbccdd", "photos/img.jpg")
	m.Namespace = "files"
	m.SessionID = "sess1"
	m.OriginalSize = 100
	m.CreatedAt = time.Now().UTC()

	if err := v.Add(m); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := v.GetIn(vw.RootByPath, "photos/img.jpg"); err != nil {
		t.Errorf("by-path: %v", err)
	}
	if _, err := v.GetIn(vw.RootByArtifact, "aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-artifact: %v", err)
	}
	if _, err := v.GetIn(vw.RootBySession, "sess1/sha256-aabbccdd"); err != nil {
		t.Errorf("by-session: %v", err)
	}
	if _, err := v.GetIn(vw.RootByNamespace, "files/aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-namespace: %v", err)
	}
	if v.Stats.TotalNodes != 1 {
		t.Errorf("TotalNodes: got %d, want 1", v.Stats.TotalNodes)
	}
}

func TestAdd_Idempotent(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	m := manifestfx.ManifestWithFsmetaPath("sha256-aabbccdd", "a")
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
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src)
	v.Close()

	err := v.Add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()))
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("expected os.ErrClosed, got %v", err)
	}
}

// --- Remove ---

func TestRemove_DropsFromAllTrees(t *testing.T) {
	src := projectionfx.New()
	m := manifestfx.ManifestWithFsmetaPath("sha256-aabbccdd", "photos/img.jpg")
	m.Namespace = "files"
	m.SessionID = "sess1"
	m.OriginalSize = 100
	m.CreatedAt = time.Now().UTC()
	src.Add(m, nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if err := v.Remove("sha256-aabbccdd"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := v.GetIn(vw.RootByPath, "photos/img.jpg"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("by-path should be gone, got %v", err)
	}
	if _, err := v.GetIn(vw.RootByArtifact, "aa/bb/sha256-aabbccdd"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("by-artifact should be gone, got %v", err)
	}
	if v.Stats.TotalNodes != 0 {
		t.Errorf("TotalNodes: got %d, want 0", v.Stats.TotalNodes)
	}
}

func TestRemove_PromotesLoserOnOwnerRemove(t *testing.T) {
	src := projectionfx.New()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	a := manifestfx.ManifestWithFsmetaPath("sha256-aaaa1111", "shared")
	a.CreatedAt = older
	b := manifestfx.ManifestWithFsmetaPath("sha256-bbbb2222", "shared")
	b.CreatedAt = newer
	src.Add(a, nil)
	src.Add(b, nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if err := v.Remove("sha256-bbbb2222"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	n, err := v.GetIn(vw.RootByPath, "shared")
	if err != nil {
		t.Fatalf("GetByPath after promote: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-aaaa1111") {
		t.Errorf("expected aaaa1111 promoted; got %q", n.Artifact.ArtifactID)
	}
}

func TestRemove_LoserDoesNotAffectOwner(t *testing.T) {
	src := projectionfx.New()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	a := manifestfx.ManifestWithFsmetaPath("sha256-aaaa1111", "shared")
	a.CreatedAt = older
	b := manifestfx.ManifestWithFsmetaPath("sha256-bbbb2222", "shared")
	b.CreatedAt = newer
	src.Add(a, nil)
	src.Add(b, nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if err := v.Remove("sha256-aaaa1111"); err != nil {
		t.Fatalf("Remove loser: %v", err)
	}
	n, err := v.GetIn(vw.RootByPath, "shared")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("owner changed: got %q", n.Artifact.ArtifactID)
	}
}

func TestRemove_Idempotent(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	if err := v.Remove("nonexistent-id"); err != nil {
		t.Errorf("Remove for unknown id should be no-op, got %v", err)
	}
}

// --- Move ---

func TestMove_RenameFile(t *testing.T) {
	src := projectionfx.New()
	src.Add(manifestfx.ManifestWithFsmetaPath("sha256-aaaa1111", "old/path.txt"), nil)
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	newM := manifestfx.ManifestWithFsmetaPath("sha256-bbbb2222", "new/path.txt")
	if err := v.Move("old/path.txt", "new/path.txt", newM); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if _, err := v.GetIn(vw.RootByPath, "old/path.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("old path should be gone, got %v", err)
	}
	n, err := v.GetIn(vw.RootByPath, "new/path.txt")
	if err != nil {
		t.Fatalf("GetByPath new: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("new artifact: got %q", n.Artifact.ArtifactID)
	}
}

// --- Filter prefix ---

func TestNewView_FilterPrefix(t *testing.T) {
	src := projectionfx.New()
	src.Add(manifestfx.ManifestWithFsmetaPath("sha256-aaaa1111", "photos/a.jpg"), nil)
	src.Add(manifestfx.ManifestWithFsmetaPath("sha256-bbbb2222", "docs/b.txt"), nil)

	v, _ := vw.New(
		context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver),
		vw.WithFilter(vw.Filter{Prefix: "photos/"}),
	)
	defer v.Close()

	if v.Stats.TotalNodes != 1 {
		t.Errorf("TotalNodes: got %d, want 1", v.Stats.TotalNodes)
	}
	if _, err := v.GetIn(vw.RootByPath, "photos/a.jpg"); err != nil {
		t.Errorf("photos: %v", err)
	}
	if _, err := v.GetIn(vw.RootByPath, "docs/b.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("docs should be filtered: got %v", err)
	}
}

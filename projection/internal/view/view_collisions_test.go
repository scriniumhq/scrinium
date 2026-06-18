package view_test

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/event"
	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/domain"
)

// withCreatedAt builds a neutral path-bearing manifest at a specific
// time, for the collision tests (freshest-wins arbitration keys on
// CreatedAt). The logical path rides in Ext under "_p" via testManifest.
func withCreatedAt(id, path string, createdAt time.Time) domain.Manifest {
	return testManifest(id, path, createdAt)
}

// --- by-path: happy path and orphaned ---

func TestByPath_HappyPath(t *testing.T) {
	src := projectionfx.New()
	src.Add(
		pathManifest("sha256-aabbccdd", "photos/2024/sunrise.jpg"),
		nil,
	)

	v, err := vw.New(
		context.Background(), src,
		vw.WithProvidedViews(testProvided()),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	defer v.Close()

	node, err := v.GetIn(testRoot, "photos/2024/sunrise.jpg")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if node.FS.IsDir {
		t.Errorf("expected file")
	}
	if node.Artifact == nil {
		t.Fatal("expected Artifact populated")
	}
	if node.Artifact.ArtifactID != domain.ArtifactID("sha256-aabbccdd") {
		t.Errorf("ArtifactID: got %q", node.Artifact.ArtifactID)
	}
}

func TestByPath_VirtualDirsExist(t *testing.T) {
	src := projectionfx.New()
	src.Add(pathManifest("sha256-aabbccdd", "photos/2024/img.jpg"), nil)
	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()))
	defer v.Close()

	for _, dir := range []string{"photos", "photos/2024"} {
		n, err := v.GetIn(testRoot, dir)
		if err != nil {
			t.Errorf("GetByPath(%q): %v", dir, err)
			continue
		}
		if !n.FS.IsDir {
			t.Errorf("%q should be a virtual dir", dir)
		}
	}
}

func TestByPath_OrphanedNotPresent(t *testing.T) {
	// Artifact without metadata → orphaned, NOT in by-path.
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()))
	defer v.Close()

	if v.Stats.OrphanedCount != 1 {
		t.Errorf("OrphanedCount: got %d, want 1", v.Stats.OrphanedCount)
	}
	count := 0
	for n, err := range v.WalkIn(testRoot, "") {
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		if n.FS.Path != "" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("by-path should be empty for orphaned artifact, got %d nodes", count)
	}
}

// --- by-path: synthetic fallback ---

func TestByPath_SyntheticFallback(t *testing.T) {
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "photos", "s12345", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()),
		vw.WithFallback(vw.FallbackSynthetic))
	defer v.Close()

	expected := "s1/23/s12345/aabbccdd.bin"
	if _, err := v.GetIn(testRoot, expected); err != nil {
		t.Fatalf("GetByPath(%q): %v", expected, err)
	}
	if v.Stats.OrphanedCount != 0 {
		t.Errorf("synthetic should not count as orphaned; got %d", v.Stats.OrphanedCount)
	}
}

func TestByPath_SyntheticAnonymous(t *testing.T) {
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "", "", 100, time.Now().UTC()), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()),
		vw.WithFallback(vw.FallbackSynthetic))
	defer v.Close()

	expected := "_anonymous/aabbccdd.bin"
	if _, err := v.GetIn(testRoot, expected); err != nil {
		t.Fatalf("GetByPath(%q): %v", expected, err)
	}
}

// --- by-path: collisions ---

func TestByPath_CollisionFresherWins(t *testing.T) {
	src := projectionfx.New()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	src.Add(withCreatedAt("sha256-aaaa1111", "shared/path.txt", older), nil)
	src.Add(withCreatedAt("sha256-bbbb2222", "shared/path.txt", newer), nil)

	bus := eventfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()),
		vw.WithEventBus(bus))
	defer v.Close()

	n, err := v.GetIn(testRoot, "shared/path.txt")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("winner: got %q, want sha256-bbbb2222", n.Artifact.ArtifactID)
	}
	if v.Stats.CollisionCount != 1 {
		t.Errorf("CollisionCount: got %d, want 1", v.Stats.CollisionCount)
	}

	// Both still reachable through by-artifact.
	if _, err := v.GetIn(vw.RootByArtifact, "aa/aa/sha256-aaaa1111"); err != nil {
		t.Errorf("loser must remain in by-artifact: %v", err)
	}
	if _, err := v.GetIn(vw.RootByArtifact, "bb/bb/sha256-bbbb2222"); err != nil {
		t.Errorf("winner must be in by-artifact: %v", err)
	}

	collisions := bus.ByType(event.EventPathCollision)
	if len(collisions) != 1 {
		t.Errorf("collision events: got %d, want 1", len(collisions))
	} else {
		p := collisions[0].Payload.(event.PathCollisionPayload)
		if p.Path != "shared/path.txt" {
			t.Errorf("payload Path: got %q", p.Path)
		}
		if p.Winner != domain.ArtifactID("sha256-bbbb2222") {
			t.Errorf("payload Winner: got %q", p.Winner)
		}
		if p.Loser != domain.ArtifactID("sha256-aaaa1111") {
			t.Errorf("payload Loser: got %q", p.Loser)
		}
	}
}

func TestByPath_CollisionEqualCreatedAt(t *testing.T) {
	// Tie-breaker: lex-larger ArtifactID wins.
	src := projectionfx.New()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	src.Add(withCreatedAt("sha256-aaaa1111", "shared", t0), nil)
	src.Add(withCreatedAt("sha256-bbbb2222", "shared", t0), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()))
	defer v.Close()

	n, _ := v.GetIn(testRoot, "shared")
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("lex-larger ID should win, got %q", n.Artifact.ArtifactID)
	}
}

func TestByPath_OrderedArrival(t *testing.T) {
	// Newer first, older second — newer keeps, older joins
	// losers. Verifies Add is symmetric to backfill order.
	src := projectionfx.New()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	src.Add(withCreatedAt("sha256-bbbb2222", "shared", newer), nil)
	src.Add(withCreatedAt("sha256-aaaa1111", "shared", older), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()))
	defer v.Close()

	n, _ := v.GetIn(testRoot, "shared")
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("winner should still be newer (bbbb2222), got %q",
			n.Artifact.ArtifactID)
	}
}

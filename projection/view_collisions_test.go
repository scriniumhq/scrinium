package projection_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/event"
	"github.com/rkurbatov/scrinium/projection"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// --- Shared helpers (used by view_collisions_test.go and
//      view_mutations_test.go) ---

// makeManifestWithMeta extends makeManifest from view_test.go
// with fsmeta-encoded metadata for the given path. Use to
// populate by-path.
func makeManifestWithMeta(id, ns, sid, path string, size int64, createdAt time.Time) domain.Manifest {
	m := makeManifest(id, ns, sid, size, createdAt)
	if path != "" {
		raw, err := fsmeta.Encode(fsmeta.FileSystem{Path: path})
		if err != nil {
			panic("makeManifestWithMeta: " + err.Error())
		}
		m.Metadata = raw
	}
	return m
}

// recordingBus collects published events for assertion. Implements
// event.EventBus.
type recordingBus struct {
	mu     sync.Mutex
	events []event.Event
}

func newRecordingBus() *recordingBus {
	return &recordingBus{}
}

func (b *recordingBus) Publish(e event.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
}

func (b *recordingBus) Subscribe(fn func(event.Event)) {
	// Not needed in tests; required by the interface.
}

// byType returns events filtered by Type, in publish order.
func (b *recordingBus) byType(t string) []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []event.Event
	for _, e := range b.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

var _ event.EventBus = (*recordingBus)(nil)

// --- by-path: happy path and orphaned ---

func TestByPath_HappyPath(t *testing.T) {
	src := newFakeSource()
	src.add(
		makeManifestWithMeta("sha256-aabbccdd", "files", "s1",
			"photos/2024/sunrise.jpg", 100, time.Now().UTC()),
		nil,
	)

	v, err := projection.NewView(
		context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	defer v.Close()

	node, err := v.GetByPath("photos/2024/sunrise.jpg")
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
	src := newFakeSource()
	src.add(
		makeManifestWithMeta("sha256-aabbccdd", "f", "s",
			"photos/2024/img.jpg", 100, time.Now().UTC()),
		nil,
	)
	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	for _, dir := range []string{"photos", "photos/2024"} {
		n, err := v.GetByPath(dir)
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
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	if v.Stats.OrphanedCount != 1 {
		t.Errorf("OrphanedCount: got %d, want 1", v.Stats.OrphanedCount)
	}
	count := 0
	for n, err := range v.WalkByPath("") {
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
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "photos", "s12345", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithFallback(projection.FallbackSynthetic))
	defer v.Close()

	// Synthetic path: <ns>/<sid-shard>/<id-short>.bin where
	// sid-shard = "s1/23/s12345" for "s12345"
	// id-short = full hash when ≤ 16 chars: "aabbccdd"
	expected := "photos/s1/23/s12345/aabbccdd.bin"
	if _, err := v.GetByPath(expected); err != nil {
		t.Fatalf("GetByPath(%q): %v", expected, err)
	}
	if v.Stats.OrphanedCount != 0 {
		t.Errorf("synthetic should not count as orphaned; got %d", v.Stats.OrphanedCount)
	}
}

func TestByPath_SyntheticAnonymous(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "", "", 100, time.Now().UTC()), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithFallback(projection.FallbackSynthetic))
	defer v.Close()

	expected := "_anonymous/aabbccdd.bin"
	if _, err := v.GetByPath(expected); err != nil {
		t.Fatalf("GetByPath(%q): %v", expected, err)
	}
}

// --- by-path: collisions ---

func TestByPath_CollisionFresherWins(t *testing.T) {
	src := newFakeSource()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	src.add(makeManifestWithMeta("sha256-aaaa1111", "f", "s",
		"shared/path.txt", 100, older), nil)
	src.add(makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"shared/path.txt", 200, newer), nil)

	bus := newRecordingBus()
	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithEventBus(bus))
	defer v.Close()

	n, err := v.GetByPath("shared/path.txt")
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
	if _, err := v.GetByArtifact("aa/aa/sha256-aaaa1111"); err != nil {
		t.Errorf("loser must remain in by-artifact: %v", err)
	}
	if _, err := v.GetByArtifact("bb/bb/sha256-bbbb2222"); err != nil {
		t.Errorf("winner must be in by-artifact: %v", err)
	}

	collisions := bus.byType(projection.EventPathCollision)
	if len(collisions) != 1 {
		t.Errorf("collision events: got %d, want 1", len(collisions))
	} else {
		p := collisions[0].Payload.(projection.PathCollisionPayload)
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
	src := newFakeSource()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	src.add(makeManifestWithMeta("sha256-aaaa1111", "f", "s",
		"shared", 100, t0), nil)
	src.add(makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"shared", 200, t0), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	n, _ := v.GetByPath("shared")
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("lex-larger ID should win, got %q", n.Artifact.ArtifactID)
	}
}

func TestByPath_OrderedArrival(t *testing.T) {
	// Newer first, older second — newer (already there) keeps,
	// older joins losers. Verifies Add is symmetric to backfill
	// order.
	src := newFakeSource()
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	src.add(makeManifestWithMeta("sha256-bbbb2222", "f", "s",
		"shared", 200, newer), nil)
	src.add(makeManifestWithMeta("sha256-aaaa1111", "f", "s",
		"shared", 100, older), nil)

	v, _ := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	n, _ := v.GetByPath("shared")
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("winner should still be newer (bbbb2222), got %q",
			n.Artifact.ArtifactID)
	}
}

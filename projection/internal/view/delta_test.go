package view_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"scrinium.dev/domain"
	src "scrinium.dev/projection/internal/source"
	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/projectionfx"
)

// deltaSource is a controllable DeltaSource: Token reports cur, Since returns
// the queued changes (never gapped) and advances to next.
type deltaSource struct {
	cur    atomic.Uint64
	mu     sync.Mutex
	queued []domain.Manifest
	next   uint64
}

func (d *deltaSource) Token(context.Context) (uint64, error) { return d.cur.Load(), nil }

func (d *deltaSource) Since(_ context.Context, _ uint64) (src.Delta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return src.Delta{Changes: d.queued, Next: d.next, Gapped: false}, nil
}

// guardWalk wraps a Provider and fails the test if Walk runs while disarmed —
// proving the incremental path avoided a full re-walk.
type guardWalk struct {
	*projectionfx.FakeSource
	t     *testing.T
	armed atomic.Bool // true ⇒ Walk allowed (the initial backfill)
}

func (g *guardWalk) Walk(ctx context.Context, cb func(domain.Manifest) error) error {
	if !g.armed.Load() {
		g.t.Error("unexpected full Walk — incremental delta should have avoided it")
	}
	return g.FakeSource.Walk(ctx, cb)
}

func TestDelta_IncrementalUpsertAvoidsWalk(t *testing.T) {
	ctx := context.Background()
	fs := projectionfx.New()
	a := makeManifest("art-a", "", 1, time.Now().UTC())
	fs.Add(a, nil)

	g := &guardWalk{FakeSource: fs, t: t}
	g.armed.Store(true)

	d := &deltaSource{}
	d.cur.Store(1)

	v, err := vw.New(ctx, g, vw.WithSyncSource(d))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer v.Close()

	// From here a full Walk is a bug — the delta must carry the change.
	g.armed.Store(false)

	b := makeManifest("art-b", "", 1, time.Now().UTC())
	fs.Add(b, nil)
	d.mu.Lock()
	d.queued = []domain.Manifest{b}
	d.next = 2
	d.mu.Unlock()
	d.cur.Store(2)

	// A read triggers convergence; the delta path upserts art-b without Walk.
	if _, ok := v.LookupLocations(b.ArtifactID); !ok {
		t.Error("art-b not visible after incremental delta convergence")
	}
	if _, ok := v.LookupLocations(a.ArtifactID); !ok {
		t.Error("art-a lost after incremental convergence")
	}
}

// gappedSource always reports a gapped delta, forcing the full-walk backstop.
type gappedSource struct{ cur atomic.Uint64 }

func (g *gappedSource) Token(context.Context) (uint64, error) { return g.cur.Load(), nil }

func (g *gappedSource) Since(_ context.Context, _ uint64) (src.Delta, error) {
	return src.Delta{Next: g.cur.Load(), Gapped: true}, nil
}

func TestDelta_GappedFallsBackToWalk(t *testing.T) {
	ctx := context.Background()
	fs := projectionfx.New()
	fs.Add(makeManifest("art-a", "", 1, time.Now().UTC()), nil)

	d := &gappedSource{}
	d.cur.Store(1)

	v, err := vw.New(ctx, fs, vw.WithSyncSource(d))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer v.Close()

	b := makeManifest("art-b", "", 1, time.Now().UTC())
	fs.Add(b, nil)
	d.cur.Store(2)

	// Gapped delta → full re-walk picks up art-b from the source.
	if _, ok := v.LookupLocations(b.ArtifactID); !ok {
		t.Error("art-b not visible after gapped fallback to full walk")
	}
}

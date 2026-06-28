package view_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/projectionfx"
)

// fakeToken is a controllable TokenSource: tests bump it to simulate another
// client advancing the backend's change-sequence.
type fakeToken struct{ v atomic.Uint64 }

func (f *fakeToken) Token(ctx context.Context) (uint64, error) { return f.v.Load(), nil }

// TestRefresh_LazyShowsForeignWrite checks that, with a SyncSource wired, a
// read after the token advances re-derives the View and surfaces an artifact
// another writer added to the source (ADR-107 lazy convergence).
func TestRefresh_LazyShowsForeignWrite(t *testing.T) {
	ctx := context.Background()
	src := projectionfx.New()
	a := makeManifest("art-a", "", 1, time.Now().UTC())
	src.Add(a, nil)

	tok := &fakeToken{}
	tok.v.Store(1)

	v, err := vw.New(ctx, src, vw.WithSyncSource(tok))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The foreign artifact is not present yet (token unchanged).
	b := makeManifest("art-b", "", 1, time.Now().UTC())
	if _, ok := v.LookupLocations(b.ArtifactID); ok {
		t.Fatal("art-b present before it was written")
	}

	// Another writer adds art-b and advances the token.
	src.Add(b, nil)
	tok.v.Store(2)

	// A read triggers the lazy refresh and now sees art-b.
	if _, ok := v.LookupLocations(b.ArtifactID); !ok {
		t.Error("art-b not visible after token advanced (lazy refresh did not fire)")
	}
	// The original artifact survives the rebuild.
	if _, ok := v.LookupLocations(a.ArtifactID); !ok {
		t.Error("art-a lost across refresh")
	}
}

// TestRefresh_SnapshotIgnoresForeignWrite checks that without a SyncSource the
// View is a snapshot as of New and does not observe later writes (INV-107-6).
func TestRefresh_SnapshotIgnoresForeignWrite(t *testing.T) {
	ctx := context.Background()
	src := projectionfx.New()
	src.Add(makeManifest("art-a", "", 1, time.Now().UTC()), nil)

	v, err := vw.New(ctx, src) // no WithSyncSource → snapshot
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	b := makeManifest("art-b", "", 1, time.Now().UTC())
	src.Add(b, nil)

	if _, ok := v.LookupLocations(b.ArtifactID); ok {
		t.Error("snapshot View observed a foreign write (INV-107-6 violated)")
	}
}

// TestRefresh_HotPathNoRebuild checks that when the token is unchanged a read
// does not re-derive: an artifact added to the source without advancing the
// token stays invisible.
func TestRefresh_HotPathNoRebuild(t *testing.T) {
	ctx := context.Background()
	src := projectionfx.New()
	src.Add(makeManifest("art-a", "", 1, time.Now().UTC()), nil)

	tok := &fakeToken{}
	tok.v.Store(5)

	v, err := vw.New(ctx, src, vw.WithSyncSource(tok))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Add without advancing the token — the View must not pick it up.
	b := makeManifest("art-b", "", 1, time.Now().UTC())
	src.Add(b, nil)

	if _, ok := v.LookupLocations(b.ArtifactID); ok {
		t.Error("read re-derived without a token change (hot path not taken)")
	}
}

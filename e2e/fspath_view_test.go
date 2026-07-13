//go:build e2e

package e2e_test

// End-to-end coverage of the by-path view as the fspath extension actually
// provides it: the real ProvidedView (its vfsmeta resolver) driven through a
// real projection.Build, asserted through the public projection.Reader.
//
// The generic placement machinery (collision arbitration, orphan routing,
// counting) is exercised against a neutral fake provided view inside the
// projection package; here we pin the extension-specific contract — that
// fspath's resolver lands artifacts at their vfsmeta path, that pathless
// artifacts orphan rather than appear under by-path, and that two artifacts
// claiming one path collide. ADR-89 keeps the format out of the projection,
// so it is tested here, in the extension that owns it.

import (
	"context"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/projection"
	"scrinium.dev/testutil/manifestfx"
	"scrinium.dev/testutil/projectionfx"
	"scrinium.dev/x/fspath"
)

// byPathProjection builds a projection whose only provided view is the real
// by-path view fspath exposes, over the supplied fake source.
func byPathProjection(t *testing.T, src *projectionfx.FakeSource) projection.Reader {
	t.Helper()
	cpv := fspath.NewIndex().ProvidedViews()
	if len(cpv) != 1 || cpv[0].Root != "by-path" {
		t.Fatalf("fspath.ProvidedViews() = %+v, want one by-path view", cpv)
	}
	pv := projection.ProvidedView{
		Root:     cpv[0].Root,
		Path:     cpv[0].Path,
		Collide:  cpv[0].Collide,
		Orphans:  cpv[0].Orphans,
		CountKey: cpv[0].CountKey,
	}
	proj, err := projection.Build(context.Background(), src, nil, projection.Config{
		Editing:        "off",
		ScratchDir:     t.TempDir(),
		RootView:       "by-path",
		ByPathFallback: "orphaned",
		ProvidedViews:  []projection.ProvidedView{pv},
	})
	if err != nil {
		t.Fatalf("projection.Build: %v", err)
	}
	r := proj.Queries()
	if r == nil {
		t.Fatal("projection.Queries() = nil")
	}
	return r
}

func TestByPath_E2E_Placement(t *testing.T) {
	src := projectionfx.New()
	src.Add(manifestfx.ManifestWithVfsmetaPath("id-a", "photos/a.jpg"), nil)
	src.Add(manifestfx.ManifestWithVfsmetaPath("id-b", "photos/holiday/b.jpg"), nil)

	r := byPathProjection(t, src)

	for _, tc := range []struct{ id, want string }{
		{"id-a", "photos/a.jpg"},
		{"id-b", "photos/holiday/b.jpg"},
	} {
		loc, ok := r.LookupLocations(domain.ArtifactID(tc.id))
		if !ok {
			t.Fatalf("LookupLocations(%q): not found", tc.id)
		}
		if got := loc.Paths["by-path"]; got != tc.want {
			t.Errorf("by-path placement for %q = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestByPath_E2E_PathlessOrphans(t *testing.T) {
	src := projectionfx.New()
	src.Add(manifestfx.ManifestWithVfsmetaPath("id-placed", "docs/readme.txt"), nil)
	// Blob carries no vfsmeta Ext, so the resolver returns ("", false):
	// with Orphans set this must route to the orphan tree, never by-path.
	src.Add(manifestfx.Blob("id-orphan", "sha256-"+repeat64('c')), nil)

	r := byPathProjection(t, src)

	loc, ok := r.LookupLocations("id-orphan")
	if !ok {
		t.Fatal("LookupLocations(id-orphan): not found")
	}
	if p, present := loc.Paths["by-path"]; present {
		t.Errorf("pathless artifact appeared under by-path = %q, want absent", p)
	}
	if _, present := loc.Paths["by-orphaned"]; !present {
		t.Errorf("pathless artifact missing from orphan tree; placements = %v", loc.Paths)
	}

	// The placed one is unaffected.
	placed, _ := r.LookupLocations("id-placed")
	if got := placed.Paths["by-path"]; got != "docs/readme.txt" {
		t.Errorf("placed artifact by-path = %q, want docs/readme.txt", got)
	}
}

func TestByPath_E2E_PathCollision(t *testing.T) {
	src := projectionfx.New()
	// Two distinct artifacts claim the same logical path: by-path is a
	// colliding tree, so exactly one owns the slot and a collision is
	// recorded. (Which one wins is the projection's freshest-wins policy,
	// covered generically elsewhere; here we pin that the extension's tree
	// is collision-arbitrated at all.)
	src.Add(manifestfx.ManifestWithVfsmetaPath("id-one", "photos/dup.jpg"), nil)
	src.Add(manifestfx.ManifestWithVfsmetaPath("id-two", "photos/dup.jpg"), nil)

	r := byPathProjection(t, src)

	// Both artifacts compute the same by-path slot: that is what makes
	// them collide. (LookupLocations reports each artifact's computed
	// placement, owner or loser alike — the won/lost arbitration lives in
	// the tree, not in this per-artifact view.)
	for _, id := range []string{"id-one", "id-two"} {
		loc, ok := r.LookupLocations(domain.ArtifactID(id))
		if !ok {
			t.Fatalf("LookupLocations(%q): not found", id)
		}
		if got := loc.Paths["by-path"]; got != "photos/dup.jpg" {
			t.Errorf("by-path placement for %q = %q, want photos/dup.jpg", id, got)
		}
	}

	// And the view recorded the collision — proof that fspath's by-path
	// view is wired Collide, so the projection arbitrated the shared slot.
	if c := r.StatsSnapshot().CollisionCount; c == 0 {
		t.Errorf("CollisionCount = 0, want > 0 for two artifacts at one path")
	}
}

func repeat64(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

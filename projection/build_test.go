// Black-box tests for the public projection wiring: Build assembles a
// working View + FSOps from a backend and Config (writable and read-only),
// propagates a backfill failure, and the Projection nil-contracts on
// Queries/Close hold. The View's read semantics themselves are covered by
// internal/view tests; these assert that Build wires them up.
package projection_test

import (
	"context"
	"errors"
	"testing"

	"scrinium.dev/projection"
	"scrinium.dev/testutil/manifestfx"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
)

var errWalk = errors.New("projection_test: walk failed")

// newSource returns a FakeSource seeded with manifests before Build, so they
// survive the synchronous backfill the View runs at construction.
func newSource(t *testing.T, manifests ...domain.Manifest) *projectionfx.FakeSource {
	t.Helper()
	src := projectionfx.New()
	for _, m := range manifests {
		src.Add(m, nil)
	}
	return src
}

// byPathConfig is the writable by-path configuration (the same shape
// viewfx.Stack uses); a test overrides fields to exercise other paths.
func byPathConfig() projection.Config {
	return projection.Config{
		Editing:  "on",
		RootView: "by-path",
		ProvidedViews: []projection.ProvidedView{
			{Root: "by-path", Path: vfsmeta.Resolver, Collide: true, Orphans: true},
		},
	}
}

func TestBuild_WiresViewAndFSOps(t *testing.T) {
	src := newSource(t, manifestfx.ManifestWithVfsmetaPath("art1", "docs/readme.txt"))
	cfg := byPathConfig()
	cfg.ScratchDir = t.TempDir()

	proj, err := projection.Build(context.Background(), src, nil, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { proj.Close() })

	if proj.View == nil {
		t.Error("Build left View nil")
	}
	if proj.FSOps == nil {
		t.Error("Build left FSOps nil for a writable projection")
	}
	r := proj.Queries()
	if r == nil {
		t.Fatal("Queries returned nil for a built projection")
	}
	// The backfill must have placed the seeded artifact, so the read side
	// is genuinely wired — not merely non-nil.
	if got := r.Search("readme", 10); len(got) == 0 {
		t.Error("seeded artifact not found via the built View's Search")
	}
}

func TestBuild_ReadOnly(t *testing.T) {
	src := newSource(t, manifestfx.ManifestWithVfsmetaPath("art1", "docs/readme.txt"))
	cfg := byPathConfig()
	cfg.ReadOnly = true // ScratchDir is ignored under ReadOnly

	proj, err := projection.Build(context.Background(), src, nil, cfg)
	if err != nil {
		t.Fatalf("Build (read-only): %v", err)
	}
	t.Cleanup(func() { proj.Close() })

	// ReadOnly governs FSOps behavior, not its presence: Build still wires
	// both ends.
	if proj.View == nil || proj.FSOps == nil {
		t.Fatalf("read-only Build left a nil part: View nil=%v, FSOps nil=%v", proj.View == nil, proj.FSOps == nil)
	}
	if proj.Queries() == nil {
		t.Error("read-only projection has no query surface")
	}
}

func TestBuild_PropagatesViewError(t *testing.T) {
	src := newSource(t)
	src.SetWalkErr(errWalk) // backfill walk fails → buildView fails
	cfg := byPathConfig()
	cfg.ScratchDir = t.TempDir()

	proj, err := projection.Build(context.Background(), src, nil, cfg)
	if err == nil {
		if proj != nil {
			proj.Close()
		}
		t.Fatal("Build returned a nil error despite a failing source walk")
	}
	if proj != nil {
		t.Error("Build returned a non-nil Projection alongside an error")
	}
}

func TestProjection_QueriesNil(t *testing.T) {
	var nilProj *projection.Projection
	if nilProj.Queries() != nil {
		t.Error("(*Projection)(nil).Queries() should be nil")
	}
	if (&projection.Projection{}).Queries() != nil {
		t.Error("Queries() on a Projection with a nil View should be nil")
	}
}

func TestProjection_Close_Safe(t *testing.T) {
	var nilProj *projection.Projection
	if err := nilProj.Close(); err != nil {
		t.Errorf("(*Projection)(nil).Close() = %v, want nil", err)
	}
	if err := (&projection.Projection{}).Close(); err != nil {
		t.Errorf("empty Projection Close() = %v, want nil", err)
	}

	// A fully built projection closes cleanly too.
	src := newSource(t, manifestfx.ManifestWithVfsmetaPath("art1", "docs/readme.txt"))
	cfg := byPathConfig()
	cfg.ScratchDir = t.TempDir()
	proj, err := projection.Build(context.Background(), src, nil, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := proj.Close(); err != nil {
		t.Errorf("built Projection Close() = %v, want nil", err)
	}
}

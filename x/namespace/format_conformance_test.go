package namespace

// End-to-end coverage of the by-namespace view as this extension actually
// provides it: the real ProvidedView (its registry-backed segment resolver
// and id-sharded layout) driven through a real projection.Build, asserted
// through the public projection.Reader.
//
// The projection's generic placement machinery is covered elsewhere against a
// neutral fake; here we pin the extension-specific format that ADR-89 keeps
// out of the projection — segment selection (registry name, verbatim nsid, or
// _default) and the <segment>/<aa>/<bb>/<id> sharding with its _short bucket.

import (
	"context"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/projection"
	"scrinium.dev/testutil/manifestfx"
	"scrinium.dev/testutil/projectionfx"
)

// nsManifest builds a blob manifest stamped with the given namespace id,
// reusing the extension's own stamper so the Ext layout matches production.
func nsManifest(t *testing.T, id string, ns NamespaceID) domain.Manifest {
	t.Helper()
	m := manifestfx.Blob(id, "sha256-"+repeat64('a'))
	ext, err := stampNSID(m.Ext, ns)
	if err != nil {
		t.Fatalf("stampNSID: %v", err)
	}
	m.Ext = ext
	return m
}

// byNamespaceProjection builds a projection whose only provided view is the
// real by-namespace view this Index exposes (its segment labels snapshotted
// from reg at build time), over the supplied fake source.
func byNamespaceProjection(t *testing.T, reg *Registry, src *projectionfx.FakeSource) projection.Reader {
	t.Helper()
	cpv := NewIndex(reg).ProvidedViews()
	if len(cpv) != 1 || cpv[0].Root != byNamespaceView {
		t.Fatalf("namespace.ProvidedViews() = %+v, want one %q view", cpv, byNamespaceView)
	}
	pv := projection.ProvidedView{
		Root:     cpv[0].Root,
		Path:     cpv[0].Path,
		Collide:  cpv[0].Collide,
		Orphans:  cpv[0].Orphans,
		CountKey: cpv[0].CountKey,
	}
	proj, err := projection.Build(context.Background(), src, nil, projection.Config{
		Editing:       "off",
		ScratchDir:    t.TempDir(),
		RootView:      byNamespaceView,
		ProvidedViews: []projection.ProvidedView{pv},
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

func TestByNamespace_E2E_SegmentAndShard(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	docs, err := reg.Create(ctx, "docs")
	if err != nil {
		t.Fatalf("Create(docs): %v", err)
	}

	src := projectionfx.New()
	// Registered namespace → segment is the registry name; normal sharding.
	src.Add(nsManifest(t, "art-aabbccddee", docs), nil)
	// No nsid stamp → the _default segment.
	src.Add(manifestfx.Blob("art-eeff00112233", "sha256-"+repeat64('b')), nil)
	// Hash part shorter than four chars → the _short bucket.
	src.Add(nsManifest(t, "x-ab", docs), nil)

	r := byNamespaceProjection(t, reg, src)

	cases := []struct{ id, want string }{
		{"art-aabbccddee", "docs/aa/bb/art-aabbccddee"},
		{"art-eeff00112233", "_default/ee/ff/art-eeff00112233"},
		{"x-ab", "docs/_short/x-ab"},
	}
	for _, tc := range cases {
		loc, ok := r.LookupLocations(domain.ArtifactID(tc.id))
		if !ok {
			t.Fatalf("LookupLocations(%q): not found", tc.id)
		}
		if got := loc.Paths[byNamespaceView]; got != tc.want {
			t.Errorf("by-namespace placement for %q = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestByNamespace_E2E_UnregisteredFallsBackToNsid(t *testing.T) {
	reg, _ := newRegistry(t)
	src := projectionfx.New()
	// A manifest stamped with an nsid the registry never minted: segment
	// falls back to the verbatim nsid rather than dropping the artifact.
	src.Add(nsManifest(t, "art-99887766", NamespaceID("ghost")), nil)

	r := byNamespaceProjection(t, reg, src)

	loc, ok := r.LookupLocations("art-99887766")
	if !ok {
		t.Fatal("LookupLocations(art-99887766): not found")
	}
	if got, want := loc.Paths[byNamespaceView], "ghost/99/88/art-99887766"; got != want {
		t.Errorf("unregistered nsid placement = %q, want %q", got, want)
	}
}

func repeat64(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

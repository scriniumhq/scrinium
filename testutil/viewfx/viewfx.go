// Package viewfx builds a ready-to-use projection stack — View +
// FSOps over an in-memory FakeSource — for tests of surfaces that
// sit on top of the projection layer (FUSE, WebDAV, webview).
//
// It is separate from projectionfx so that projectionfx stays free
// of the projection import: projectionfx supplies the fakes
// (FakeSource, FakeReadHandle) that even the projection package's own
// tests rely on, and must not depend on projection. viewfx is the
// consumer-facing layer that wires those fakes into a real View +
// FSOps, so it may import projection.
package viewfx

import (
	"strings"
	"testing"

	"scrinium.dev/projection"
	"scrinium.dev/projection/vfs"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
)

// Stack wires an in-memory FakeSource into a built Projection (View +
// FSOps) with editing enabled and the "files" namespace — the
// configuration fuse and webdav share. Manifests are added to the
// source BEFORE Build so they survive the synchronous backfill the
// View performs at construction; adding to the returned source
// afterwards affects only Get/Put paths, not the built View trees.
//
// The Projection is closed via t.Cleanup. A surface that needs a
// read-only or different-namespace projection builds it directly.
//
// Stack returns the bundle, not its parts: callers (surface tests)
// hand it straight to vfs.New and never touch the projection's
// internal View/Ops types.
func Stack(t testing.TB, manifests ...domain.Manifest) (*projection.Projection, *projectionfx.FakeSource) {
	t.Helper()
	src := projectionfx.New()
	for _, m := range manifests {
		src.Add(m, nil)
	}

	proj, err := projection.Build(t.Context(), src, nil, projection.Config{
		Namespace:  "files",
		Editing:    "on",
		ScratchDir: t.TempDir(),
		ProvidedViews: []projection.ProvidedView{
			{Root: "by-path", Path: vfsmeta.Resolver, Collide: true, Orphans: true},
			byNamespaceProvided(),
		},
	})
	if err != nil {
		t.Fatalf("viewfx.Stack: Build: %v", err)
	}
	t.Cleanup(func() { proj.Close() })

	return proj, src
}

// byNamespaceProvided mirrors (for fixtures) the by-namespace layout the
// namespace extension contributes in production, keyed off the manifest
// namespace so surfaces built via Stack keep the by-namespace tree they
// had before the view stopped hardcoding it.
func byNamespaceProvided() projection.ProvidedView {
	shard := func(id string) string {
		h := id
		if i := strings.IndexByte(id, '-'); i >= 0 {
			h = id[i+1:]
		}
		if len(h) < 4 {
			return "_short/" + id
		}
		return h[:2] + "/" + h[2:4] + "/" + id
	}
	return projection.ProvidedView{
		Root: "by-namespace",
		Path: func(m domain.Manifest) (string, bool) {
			ns := m.Namespace
			if ns == "" {
				ns = "_default"
			}
			return ns + "/" + shard(string(m.ArtifactID)), true
		},
		CountKey: func(m domain.Manifest) (string, bool) {
			return m.Namespace, m.Namespace != ""
		},
	}
}

// RoutingAll returns a RoutingConfig with every service tree enabled
// under the conventional "_scrinium" prefix and RootByPath as the
// root view — the literal fuse, webdav, and the projection routing
// tests previously each declared inline.
//
// ShowRaw stays false: the raw tree is opt-in per surface (webview
// turns it on, fuse/webdav do not), so a test that needs it flips the
// field on the returned value.
func RoutingAll() vfs.Config {
	return vfs.Config{
		ServicePrefix:   "_scrinium",
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         false,
	}
}

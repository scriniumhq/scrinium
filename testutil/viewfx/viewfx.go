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
		Editing:    "on",
		ScratchDir: t.TempDir(),
		RootView:   "by-path",
		ProvidedViews: []projection.ProvidedView{
			{Root: "by-path", Path: vfsmeta.Resolver, Collide: true, Orphans: true},
		},
	})
	if err != nil {
		t.Fatalf("viewfx.Stack: Build: %v", err)
	}
	t.Cleanup(func() { proj.Close() })

	return proj, src
}

// RoutingAll returns a RoutingConfig with every service tree enabled
// under the conventional "_scrinium" prefix (the root view is set on the
// projection, not here) — the literal fuse, webdav, and the projection
// routing tests previously each declared inline.
//
// ShowRaw stays false: the raw tree is opt-in per surface (webview
// turns it on, fuse/webdav do not), so a test that needs it flips the
// field on the returned value.
func RoutingAll() vfs.Config {
	return vfs.Config{
		ServicePrefix:     "_scrinium",
		ShowStats:         true,
		ShowByArtifact:    true,
		ShowOrphaned:      true,
		ShowByDate:        true,
		ShowBySession:     true,
		ShowProvidedViews: true,
		ShowRaw:           false,
	}
}

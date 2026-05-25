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
	"context"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/projection/fsmeta"
	"scrinium.dev/internal/testutil/projectionfx"
)

// Stack wires an in-memory FakeSource into a View + FSOps with the
// fsmeta path resolver and editing enabled. Manifests are added to
// the source BEFORE NewView so they survive the synchronous backfill
// the View performs at construction; adding to the returned source
// afterwards affects only Get/Put paths, not the built View trees.
//
// The View is closed via t.Cleanup and the scratch dir is a fresh
// t.TempDir(). Editing is on and the namespace is "files" — the
// configuration fuse and webdav share. A surface that needs a
// read-only or different-namespace FSOps builds it directly.
func Stack(t testing.TB, manifests ...domain.Manifest) (*projection.View, *projection.FSOps, *projectionfx.FakeSource) {
	t.Helper()
	src := projectionfx.New()
	for _, m := range manifests {
		src.Add(m, nil)
	}

	v, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("viewfx.Stack: NewView: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	o, err := projection.NewFSOps(v,
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
		projection.WithEditingPolicy(projection.EditingOn()),
	)
	if err != nil {
		t.Fatalf("viewfx.Stack: NewFSOps: %v", err)
	}
	return v, o, src
}

// RoutingAll returns a RoutingConfig with every service tree enabled
// under the conventional "_scrinium" prefix and RootByPath as the
// root view — the literal fuse, webdav, and the projection routing
// tests previously each declared inline.
//
// ShowRaw stays false: the raw tree is opt-in per surface (webview
// turns it on, fuse/webdav do not), so a test that needs it flips the
// field on the returned value.
func RoutingAll() projection.RoutingConfig {
	return projection.RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        projection.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         false,
	}
}

package vfs_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"scrinium.dev/projection/node"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/internal/testutil/projectionfx"
	"scrinium.dev/projection"
	"scrinium.dev/projection/fsmeta"
	"scrinium.dev/projection/vfs"
)

// newTestVFS builds a VFS over an in-memory FakeSource. The
// caller passes manifests to seed before NewView so they
// participate in backfill.
//
// Defaults match the most common surface configuration:
// service trees enabled, by-path root, namespace "files".
// Tests that need different semantics override per-call.
func newTestVFS(t *testing.T, manifests ...domain.Manifest) (*vfs.VFS, *projectionfx.FakeSource) {
	t.Helper()
	src := projectionfx.New()
	for _, m := range manifests {
		src.Add(m, nil)
	}
	v, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	o, err := projection.NewFSOps(v,
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
		projection.WithEditingPolicy(projection.EditingOn()),
	)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}

	cfg := projection.RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        node.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
	}
	return vfs.New(v, o, cfg), src
}

// --- CleanPath ---

func TestCleanPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"/foo", "foo"},
		{"foo/", "foo"},
		{"/foo/bar/", "foo/bar"},
		{"foo/bar", "foo/bar"},
	}
	for _, tc := range cases {
		got := vfs.CleanPath(tc.in)
		if got != tc.want {
			t.Errorf("CleanPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- Stat / OpenFile basics ---

func TestVFS_StatRoot(t *testing.T) {
	v, _ := newTestVFS(t)
	fi, err := v.Stat(context.Background(), "/")
	if err != nil {
		t.Fatalf("Stat /: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("root must be a directory")
	}
}

func TestVFS_StatServiceRoot(t *testing.T) {
	v, _ := newTestVFS(t)
	fi, err := v.Stat(context.Background(), "/_scrinium")
	if err != nil {
		t.Fatalf("Stat /_scrinium: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("_scrinium must be a directory")
	}
	if fi.Name() != "_scrinium" {
		t.Errorf("Name: got %q", fi.Name())
	}
}

func TestVFS_StatStatsFile(t *testing.T) {
	v, _ := newTestVFS(t)
	fi, err := v.Stat(context.Background(), "/_scrinium/stats")
	if err != nil {
		t.Fatalf("Stat stats: %v", err)
	}
	if fi.IsDir() {
		t.Errorf("stats must be a file")
	}
	if fi.Size() == 0 {
		t.Errorf("stats file should have a non-empty body")
	}
}

func TestVFS_StatNonExistent(t *testing.T) {
	v, _ := newTestVFS(t)
	_, err := v.Stat(context.Background(), "/does/not/exist")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error should be fs.ErrNotExist, got %v", err)
	}
}

func TestVFS_OpenAndReadStats(t *testing.T) {
	v, _ := newTestVFS(t)
	f, err := v.OpenFile(context.Background(), "/_scrinium/stats", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile stats: %v", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) == 0 {
		t.Errorf("stats body is empty")
	}
}

// --- Service tree visibility from prefix listing ---

func TestVFS_ServicePrefixListing(t *testing.T) {
	v, _ := newTestVFS(t)
	d, err := v.OpenFile(context.Background(), "/_scrinium", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile _scrinium: %v", err)
	}
	defer d.Close()
	infos, err := d.Readdir(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Readdir: %v", err)
	}
	names := make(map[string]bool)
	for _, fi := range infos {
		names[fi.Name()] = true
	}
	want := []string{"stats", "by-path", "by-date", "by-session", "by-namespace", "by-artifact", "orphaned"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("listing missing %q (got %v)", w, infos)
		}
	}
}

// --- ServicePrefix=off omits the prefix dir ---

func TestVFS_NoServicePrefix(t *testing.T) {
	src := projectionfx.New()
	view, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	t.Cleanup(func() { view.Close() })
	ops, err := projection.NewFSOps(view,
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}
	v := vfs.New(view, ops, projection.RoutingConfig{
		ServicePrefix: "", // disabled
		RootView:      node.RootByPath,
	})

	// _scrinium must not exist when service prefix is empty.
	_, err = v.Stat(context.Background(), "/_scrinium")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("_scrinium should not resolve when ServicePrefix is empty: %v", err)
	}
}

// --- Service trees are read-only ---

func TestVFS_ServiceWriteRejected(t *testing.T) {
	v, _ := newTestVFS(t)
	_, err := v.OpenFile(context.Background(), "/_scrinium/by-date/test", os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		t.Fatal("expected error writing into service tree")
	}
	// Either ErrPermission or ErrNotExist depending on
	// whether routing rejects the path before or after the
	// write check.
	if !errors.Is(err, fs.ErrPermission) && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected ErrPermission/ErrNotExist, got %v", err)
	}
}

func TestVFS_ServiceMkdirRejected(t *testing.T) {
	v, _ := newTestVFS(t)
	err := v.Mkdir(context.Background(), "/_scrinium/foo", 0o755)
	if err == nil {
		t.Fatal("expected error mkdir under service prefix")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("expected ErrPermission, got %v", err)
	}
}

// --- Stats provider injection ---

func TestVFS_StatsProvider(t *testing.T) {
	src := projectionfx.New()
	view, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	t.Cleanup(func() { view.Close() })
	ops, err := projection.NewFSOps(view,
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}

	const customBody = "stats injected by test"
	v := vfs.New(view, ops, projection.RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        node.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
	}, vfs.WithStatsProvider(func() []byte { return []byte(customBody) }))

	f, err := v.OpenFile(context.Background(), "/_scrinium/stats", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile stats: %v", err)
	}
	defer f.Close()
	body, _ := io.ReadAll(f)
	if string(body) != customBody {
		t.Errorf("stats body mismatch: got %q, want %q", body, customBody)
	}
}

// --- NameFilter ---

func TestVFS_NameFilter_OmitsFromListing(t *testing.T) {
	// Seed an artifact at a junk-named path; with NameFilter
	// active, it should not appear in Readdir output.
	man := mkManifest(".DS_Store", "files", "ds")
	src := projectionfx.New()
	src.Add(man, nil)

	view, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	t.Cleanup(func() { view.Close() })
	ops, err := projection.NewFSOps(view,
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}

	// VFS with a filter that suppresses .DS_Store.
	filter := func(name string) bool { return name == ".DS_Store" }
	v := vfs.New(view, ops, projection.RoutingConfig{
		RootView: node.RootByPath,
	}, vfs.WithNameFilter(filter))

	d, err := v.OpenFile(context.Background(), "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile /: %v", err)
	}
	defer d.Close()
	infos, _ := d.Readdir(-1)
	for _, fi := range infos {
		if fi.Name() == ".DS_Store" {
			t.Errorf("filter should have suppressed .DS_Store; got %v", infos)
		}
	}
}

// --- Helpers ---

// mkManifest builds a minimal Manifest with the given path
// embedded in fsmeta.
func mkManifest(path, namespace, payload string) domain.Manifest {
	id := domain.ArtifactID(strings.ReplaceAll(path, "/", "_") + "_id")
	extMeta, _ := fsmeta.Encode(fsmeta.FileSystem{
		Kind: fsmeta.Marker,
		Path: path,
		Mode: 0o644,
	})
	return domain.Manifest{
		ArtifactID:   id,
		Type:         domain.ManifestTypeBlob,
		Namespace:    namespace,
		Ext:          extMeta,
		OriginalSize: int64(len(payload)),
	}
}

// These cases moved here from cmd/scrinium-fuse when the FUSE layer
// was reduced to a thin adapter over the VFS facade. The behaviour
// they cover — service-tree visibility under Show* flags and the
// content of the stats virtual file — is owned by the VFS, so it is
// tested at this layer rather than through a FUSE node.

// ServicePrefix listing omits trees whose Show* flag is off.
func TestVFS_ServicePrefixListing_SkipsDisabled(t *testing.T) {
	src := projectionfx.New()
	view, err := projection.NewView(context.Background(), src,
		projection.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	t.Cleanup(func() { view.Close() })
	ops, err := projection.NewFSOps(view,
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}
	// by-session and by-date disabled; the rest on.
	v := vfs.New(view, ops, projection.RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        node.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByNamespace: true,
	})

	d, err := v.OpenFile(context.Background(), "/_scrinium", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile _scrinium: %v", err)
	}
	defer d.Close()
	infos, err := d.Readdir(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Readdir: %v", err)
	}
	for _, fi := range infos {
		if fi.Name() == "by-session" || fi.Name() == "by-date" {
			t.Errorf("disabled tree %q present in listing", fi.Name())
		}
	}
}

// The stats virtual file renders the View counters; at minimum it
// names the TotalNodes field.
func TestVFS_StatsBodyMentionsTotalNodes(t *testing.T) {
	v, _ := newTestVFS(t)
	f, err := v.OpenFile(context.Background(), "/_scrinium/stats", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile stats: %v", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), "TotalNodes") {
		t.Errorf("stats body missing TotalNodes:\n%s", body)
	}
}

// --- Compile-time sanity ---

var _ vfs.File = (vfs.File)(nil)

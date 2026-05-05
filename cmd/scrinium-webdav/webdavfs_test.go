package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/testutil/projectionfx"
	"github.com/rkurbatov/scrinium/projection"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// newTestFS builds a webdavFS wired against an in-memory
// FakeSource. Manifests passed in are added BEFORE NewView so
// they survive backfill.
func newTestFS(t *testing.T, manifests ...domain.Manifest) (*webdavFS, *projectionfx.FakeSource) {
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

	return newWebdavFS(v, o, projection.RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        projection.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         false,
	}, true /* rejectJunk */), src
}

// --- cleanWebDAVPath ---

func TestCleanWebDAVPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"/photos", "photos"},
		{"/photos/", "photos"},
		{"/photos/img.jpg", "photos/img.jpg"},
		{"photos/img.jpg", "photos/img.jpg"},
	}
	for _, tc := range cases {
		if got := cleanWebDAVPath(tc.in); got != tc.want {
			t.Errorf("cleanWebDAVPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- isAtServiceRoot ---

func TestIsAtServiceRoot(t *testing.T) {
	cfg := projection.RoutingConfig{ServicePrefix: "_scrinium"}
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{"photos", false},
		{"_scrinium", true},
		{"_scrinium/by-session", true},
		{"_scrinium/anything", true},
		{"photos/_scrinium", false},
	}
	for _, tc := range cases {
		if got := isAtServiceRoot(tc.path, cfg); got != tc.want {
			t.Errorf("isAtServiceRoot(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsAtServiceRoot_PrefixDisabled(t *testing.T) {
	cfg := projection.RoutingConfig{ServicePrefix: ""}
	if isAtServiceRoot("_scrinium/x", cfg) {
		t.Error("with empty prefix, isAtServiceRoot must always be false")
	}
}

// --- Stat ---

func TestStat_Root(t *testing.T) {
	w, _ := newTestFS(t)
	fi, err := w.Stat(context.Background(), "/")
	if err != nil {
		t.Fatalf("Stat /: %v", err)
	}
	if !fi.IsDir() {
		t.Error("root must be a directory")
	}
}

func TestStat_File(t *testing.T) {
	w, _ := newTestFS(t,
		projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd", "alpha"))
	fi, err := w.Stat(context.Background(), "/alpha")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir() {
		t.Error("expected file")
	}
	if fi.Name() != "alpha" {
		t.Errorf("Name: got %q", fi.Name())
	}
}

func TestStat_NotFound(t *testing.T) {
	w, _ := newTestFS(t)
	_, err := w.Stat(context.Background(), "/nope")
	if err != fs.ErrNotExist {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestStat_ServiceRoot(t *testing.T) {
	w, _ := newTestFS(t)
	fi, err := w.Stat(context.Background(), "/_scrinium")
	if err != nil {
		t.Fatalf("Stat _scrinium: %v", err)
	}
	if !fi.IsDir() {
		t.Error("_scrinium must be a directory")
	}
	if fi.Name() != "_scrinium" {
		t.Errorf("Name: got %q", fi.Name())
	}
}

func TestStat_StatsFile(t *testing.T) {
	w, _ := newTestFS(t)
	fi, err := w.Stat(context.Background(), "/_scrinium/stats")
	if err != nil {
		t.Fatalf("Stat stats: %v", err)
	}
	if fi.IsDir() {
		t.Error("stats must be a file")
	}
	if fi.Size() == 0 {
		t.Error("stats body must be non-empty")
	}
}

// --- Mkdir ---

func TestMkdir_PendingDir(t *testing.T) {
	w, _ := newTestFS(t)
	err := w.Mkdir(context.Background(), "/photos", 0o755)
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	fi, err := w.Stat(context.Background(), "/photos")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir() {
		t.Error("expected dir")
	}
}

func TestMkdir_AtServiceRoot_Forbidden(t *testing.T) {
	w, _ := newTestFS(t)
	err := w.Mkdir(context.Background(), "/_scrinium/inside", 0o755)
	if err != fs.ErrPermission {
		t.Errorf("expected fs.ErrPermission, got %v", err)
	}
}

// --- OpenFile + Read ---

func TestOpenFile_ReadFile(t *testing.T) {
	w, _ := newTestFS(t)

	// Create a file via WebDAV, then re-open and verify content.
	flag := os.O_WRONLY | syscallOCreate | os.O_TRUNC
	wf, err := w.OpenFile(context.Background(), "/hello.txt", flag, 0o644)
	if err != nil {
		t.Fatalf("OpenFile create: %v", err)
	}
	if _, err := wf.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := w.OpenFile(context.Background(), "/hello.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	defer f.Close()

	buf, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(buf) != "hello" {
		t.Errorf("content: got %q, want hello", buf)
	}
}

// --- OpenFile + Create + Write + Close roundtrip ---

func TestOpenFile_CreateWriteRead(t *testing.T) {
	w, _ := newTestFS(t)
	flag := os.O_WRONLY | syscallOCreate | os.O_TRUNC

	wf, err := w.OpenFile(context.Background(), "/note.txt", flag, 0o644)
	if err != nil {
		t.Fatalf("OpenFile create: %v", err)
	}
	if _, err := wf.Write([]byte("body")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-stat to confirm visibility.
	fi, err := w.Stat(context.Background(), "/note.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != 4 {
		t.Errorf("Size: got %d, want 4", fi.Size())
	}
}

// --- Readdir on root ---

func TestRootDir_ListsServicePrefix(t *testing.T) {
	w, _ := newTestFS(t,
		projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd", "alpha"))

	f, err := w.OpenFile(context.Background(), "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile /: %v", err)
	}
	defer f.Close()

	entries, err := f.Readdir(-1)
	if err != nil && err != io.EOF {
		t.Fatalf("Readdir: %v", err)
	}
	hasService := false
	hasAlpha := false
	for _, e := range entries {
		if e.Name() == "_scrinium" {
			hasService = true
		}
		if e.Name() == "alpha" {
			hasAlpha = true
		}
	}
	if !hasService {
		t.Errorf("_scrinium missing: %v", names(entries))
	}
	if !hasAlpha {
		t.Errorf("alpha missing: %v", names(entries))
	}
}

// --- Service tree listing ---

func TestServiceRoot_ListsTrees(t *testing.T) {
	w, _ := newTestFS(t)
	f, err := w.OpenFile(context.Background(), "/_scrinium", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile _scrinium: %v", err)
	}
	defer f.Close()

	entries, err := f.Readdir(-1)
	if err != nil && err != io.EOF {
		t.Fatalf("Readdir: %v", err)
	}
	want := []string{"stats", "by-artifact", "by-date", "by-session", "by-namespace", "orphaned", "by-path"}
	got := names(entries)
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing %q in service root listing: %v", w, got)
		}
	}
}

// --- Stats body ---

func TestStatsFile_BodyHasTotalNodes(t *testing.T) {
	w, _ := newTestFS(t)
	f, err := w.OpenFile(context.Background(), "/_scrinium/stats", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile stats: %v", err)
	}
	defer f.Close()

	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}
	if !strings.Contains(string(body), "TotalNodes") {
		t.Errorf("stats body missing TotalNodes: %s", body)
	}
}

// --- helpers ---

func names(entries []os.FileInfo) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// --- isOSJunk ---

func TestIsOSJunk(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Apple
		{".DS_Store", true},
		{"._photo.jpg", true},
		{".Spotlight-V100", true},
		{".Trashes", true},
		{".fseventsd", true},
		{".AppleDouble", true},
		{"Network Trash Folder", true},
		// Windows
		{"Thumbs.db", true},
		{"desktop.ini", true},
		{"$RECYCLE.BIN", true},
		{"~$Document.docx", true},
		// Path with junk in last segment
		{"photos/.DS_Store", true},
		{"deep/nested/._image", true},
		// Non-junk
		{"photo.jpg", false},
		{"normal_file", false},
		{".gitignore", false},
		{".env", false},
		{"_scrinium", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isOSJunk(tc.name); got != tc.want {
			t.Errorf("isOSJunk(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- Filter behaviour ---

func TestOpenFile_JunkCreateBlackHole(t *testing.T) {
	// AppleDouble sidecars must accept writes (and silently drop
	// them) — otherwise macOS Finder aborts copies. The store
	// must remain unaffected.
	w, _ := newTestFS(t)
	flag := os.O_WRONLY | syscallOCreate | os.O_TRUNC

	f, err := w.OpenFile(context.Background(), "/.DS_Store", flag, 0o644)
	if err != nil {
		t.Fatalf("expected ok for junk create (black hole), got %v", err)
	}
	if _, err := f.Write([]byte("data")); err != nil {
		t.Errorf("Write to black hole: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// Also for AppleDouble names.
	f, err = w.OpenFile(context.Background(), "/photos/._cover.jpg", flag, 0o644)
	if err != nil {
		t.Fatalf("AppleDouble create: %v", err)
	}
	f.Close()

	// Junk must remain invisible: Stat says 404.
	if _, err := w.Stat(context.Background(), "/.DS_Store"); err != fs.ErrNotExist {
		t.Errorf("Stat after black-hole PUT must still be ENOENT, got %v", err)
	}
}

func TestOpenFile_JunkReadIsNotFound(t *testing.T) {
	w, _ := newTestFS(t)
	_, err := w.OpenFile(context.Background(), "/.DS_Store", os.O_RDONLY, 0)
	if err != fs.ErrNotExist {
		t.Errorf("expected fs.ErrNotExist for .DS_Store read, got %v", err)
	}
}

func TestStat_JunkIsNotFound(t *testing.T) {
	w, _ := newTestFS(t)
	_, err := w.Stat(context.Background(), "/.DS_Store")
	if err != fs.ErrNotExist {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestMkdir_RejectsJunk(t *testing.T) {
	w, _ := newTestFS(t)
	err := w.Mkdir(context.Background(), "/.Trashes", 0o755)
	if err != fs.ErrPermission {
		t.Errorf("expected fs.ErrPermission, got %v", err)
	}
}

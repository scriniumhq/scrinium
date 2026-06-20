package localfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

// This file holds the localfs-SPECIFIC (glass-box) tests: construction,
// path resolution, the file:// scheme, on-disk tombstone mechanics,
// dotfile/temp filtering, empty-directory pruning, and the exact
// Capabilities mask. The backend-independent contract (Put/Get, ReadAt,
// Remove/Rename/Clone, Stat/List/iteration, the tombstone lifecycle) is
// exercised by the shared suite in conformance_test.go.

// helper: spin up a fresh driver in a t.TempDir() with fsync off so
// tests stay fast on slow CI machines. Tests that need fsync semantics
// call New directly.
func newTestDriver(t *testing.T) *Driver {
	t.Helper()
	d, err := New(t.TempDir(), WithFsync(false))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// --- Construction ---

func TestNew_CreatesMissingRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "newroot")
	d, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(d.Root()); err != nil {
		t.Fatalf("root not created: %v", err)
	}
}

func TestNew_RejectsNonDirectory(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "f")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file); err == nil {
		t.Fatal("expected error on non-directory root")
	}
}

func TestNew_RejectsEmptyRoot(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error on empty root")
	}
}

func TestNew_AbsolutePathReturned(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel := "_relative_test_root_"
	defer os.RemoveAll(filepath.Join(wd, rel))

	d, err := New(rel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !filepath.IsAbs(d.Root()) {
		t.Fatalf("Root is not absolute: %q", d.Root())
	}
}

// --- Path safety (resolve): rejection phrasing is localfs-specific ---

func TestPathSafety_RejectsAbsolute(t *testing.T) {
	d := newTestDriver(t)
	err := d.Put(context.Background(), "/etc/passwd", strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected absolute-path rejection, got: %v", err)
	}
}

func TestPathSafety_RejectsTraversal(t *testing.T) {
	d := newTestDriver(t)
	err := d.Put(context.Background(), "../escape", strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal rejection, got: %v", err)
	}
}

func TestPathSafety_RejectsEmpty(t *testing.T) {
	d := newTestDriver(t)
	err := d.Put(context.Background(), "", strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty rejection, got: %v", err)
	}
}

// --- Open (file:// URI): which schemes are supported is backend-specific ---

func TestOpen_FileURI(t *testing.T) {
	tmp := t.TempDir()
	abs := filepath.Join(tmp, "external.txt")
	if err := os.WriteFile(abs, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDriver(t)
	r, err := d.Open(context.Background(), "file://"+abs)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestOpen_UnsupportedScheme(t *testing.T) {
	d := newTestDriver(t)
	_, err := d.Open(context.Background(), "s3://bucket/key")
	if !errors.Is(err, errs.ErrUnsupportedURIScheme) {
		t.Fatalf("expected errs.ErrUnsupportedURIScheme, got %v", err)
	}
}

// --- List: dotfile / tombstone filtering (on-disk markers) ---

func TestList_FiltersHidden(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "dir/visible", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	// Drop a tombstone marker directly to simulate a tombstoned file.
	if err := os.WriteFile(filepath.Join(d.Root(), "dir", "tombstoned"+tombstoneSuffix), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Drop a temp-file remnant.
	if err := os.WriteFile(filepath.Join(d.Root(), "dir", ".visible.tmp.deadbeef"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := d.List(ctx, "dir")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0], "/visible") {
		t.Fatalf("unexpected list: %v", entries)
	}
}

// --- ListObjectsWithModTime: since-filter via a backdated mtime ---

func TestListObjectsWithModTime_FiltersBySince(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "old", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	// Backdate the old file to one hour ago.
	past := time.Now().Add(-time.Hour)
	oldPath := filepath.Join(d.Root(), "old")
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "new", strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}

	since := time.Now().Add(-30 * time.Minute)
	var paths []string
	err := d.ListObjectsWithModTime(ctx, "", since, func(m driver.ObjectMeta) error {
		paths = append(paths, m.Path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "new" {
		t.Fatalf("got %v, want [new]", paths)
	}
}

// --- Tombstone on-disk mechanism (rename to "<path>.tombstone") ---

func TestMarkTombstone_RenamesExisting(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkTombstone(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	// Mechanism: the original is renamed to "<path>.tombstone" via
	// rename(2) — the original is gone and the marker holds the
	// preserved content (kept for forensics and Recovery).
	if _, err := os.Stat(filepath.Join(d.Root(), "f")); !os.IsNotExist(err) {
		t.Fatalf("original still on disk: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(d.Root(), "f"+tombstoneSuffix))
	if err != nil {
		t.Fatalf("marker not found: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("marker content: got %q, want %q", got, "data")
	}
}

func TestMarkTombstone_CreatesEmptyMarkerWhenMissing(t *testing.T) {
	d := newTestDriver(t)
	if err := d.MarkTombstone(context.Background(), "never_existed"); err != nil {
		t.Fatal(err)
	}
	// Mechanism: with no original, an empty marker file is created so a
	// future IsTombstone reports the deletion intent.
	info, err := os.Stat(filepath.Join(d.Root(), "never_existed"+tombstoneSuffix))
	if err != nil {
		t.Fatalf("marker not created: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("marker should be empty, got %d bytes", info.Size())
	}
}

// --- PruneEmptyDirs (empty-subtree removal; a filesystem concept) ---

func TestPruneEmptyDirs(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	// Build a tree with a deep empty subtree and one populated leaf.
	if err := d.Put(ctx, "kept/file", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.Root(), "drop", "deep", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := d.PruneEmptyDirs(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Root(), "drop")); !os.IsNotExist(err) {
		t.Fatalf("empty subtree was not pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.Root(), "kept")); err != nil {
		t.Fatalf("kept tree was disturbed: %v", err)
	}
	if _, err := os.Stat(d.Root()); err != nil {
		t.Fatalf("root was removed: %v", err)
	}
}

// --- Capabilities (exact localfs mask) ---

func TestCapabilities(t *testing.T) {
	d := newTestDriver(t)
	caps := d.Capabilities()
	if !caps.Has(driver.CapBlockAlign4096) {
		t.Error("missing CapBlockAlign4096")
	}
	if !caps.Has(driver.CapWatch) {
		t.Error("missing CapWatch")
	}
	if caps.Has(driver.CapSlowRead) {
		t.Error("local FS must not declare CapSlowRead")
	}
	if caps.Has(driver.CapNativeChecksum) {
		t.Error("ext4/xfs default: must not declare CapNativeChecksum")
	}
}

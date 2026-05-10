package localfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
)

// helper: spin up a fresh driver in a t.TempDir() with fsync off
// so tests stay fast on slow CI machines. Tests that need fsync
// semantics call New directly.
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

// --- Path safety ---

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

func TestPathSafety_NestedPathsOK(t *testing.T) {
	d := newTestDriver(t)
	if err := d.Put(context.Background(), "a/b/c/d.txt", strings.NewReader("ok")); err != nil {
		t.Fatalf("nested put: %v", err)
	}
	r, err := d.Get(context.Background(), "a/b/c/d.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "ok" {
		t.Fatalf("got %q, want %q", got, "ok")
	}
}

// --- Put / Get round-trip ---

func TestPutGet_RoundTrip(t *testing.T) {
	d := newTestDriver(t)
	payload := []byte("hello, scrinium")

	if err := d.Put(context.Background(), "blob/x", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r, err := d.Get(context.Background(), "blob/x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestGet_NotFound(t *testing.T) {
	d := newTestDriver(t)
	_, err := d.Get(context.Background(), "missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestPut_Overwrite(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("first")); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "f", strings.NewReader("second")); err != nil {
		t.Fatal(err)
	}
	r, _ := d.Get(ctx, "f")
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "second" {
		t.Fatalf("got %q, want %q", got, "second")
	}
}

// TestPut_AtomicityUnderRead is the core safety test: a parallel
// Get during a long Put never observes a partial file. It must
// either return ErrNotExist (Put has not committed yet) or read
// the previous content in full (commit happened mid-read of the
// old file — which is impossible because we only see a snapshot
// of one inode through Open).
func TestPut_AtomicityUnderRead(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()

	// Seed the file with a known "old" payload.
	oldPayload := bytes.Repeat([]byte("OLD"), 100)
	if err := d.Put(ctx, "live", bytes.NewReader(oldPayload)); err != nil {
		t.Fatal(err)
	}

	// New payload of a clearly different size and content.
	newPayload := bytes.Repeat([]byte("NEW"), 5000)

	var wg sync.WaitGroup
	var partial atomic.Bool

	// Reader: many parallel Get calls; verifies the file is either
	// fully OLD or fully NEW.
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r, err := d.Get(ctx, "live")
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				data, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Errorf("ReadAll: %v", err)
					return
				}
				if !bytes.Equal(data, oldPayload) && !bytes.Equal(data, newPayload) {
					partial.Store(true)
					return
				}
			}
		}()
	}

	// Writer: a single Put that overwrites the file.
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Tiny sleep so readers warm up first.
		time.Sleep(2 * time.Millisecond)
		if err := d.Put(ctx, "live", bytes.NewReader(newPayload)); err != nil {
			t.Errorf("Put: %v", err)
		}
	}()

	wg.Wait()

	if partial.Load() {
		t.Fatal("observed a partial read during Put — atomicity broken")
	}
}

// --- Remove / Rename ---

func TestRemove_Idempotent(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(ctx, "f"); err != nil {
		t.Fatalf("Remove (existing): %v", err)
	}
	if err := d.Remove(ctx, "f"); err != nil {
		t.Fatalf("Remove (missing): %v", err)
	}
}

func TestRename(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "src", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if err := d.Rename(ctx, "src", "deeper/dst"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	r, err := d.Get(ctx, "deeper/dst")
	if err != nil {
		t.Fatalf("Get after rename: %v", err)
	}
	r.Close()
	if _, err := d.Stat(ctx, "src"); !os.IsNotExist(err) {
		t.Fatalf("src still exists: %v", err)
	}
}

// --- ReadAt ---

func TestReadAt_Range(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	payload := []byte("0123456789abcdef")
	if err := d.Put(ctx, "f", bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	r, err := d.ReadAt(ctx, "f", 4, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, []byte("456789")) {
		t.Fatalf("got %q, want %q", got, "456789")
	}
}

func TestReadAt_PastEnd(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	r, err := d.ReadAt(ctx, "f", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "bc" {
		t.Fatalf("got %q, want %q", got, "bc")
	}
}

func TestReadAt_NegativeOffset(t *testing.T) {
	d := newTestDriver(t)
	if _, err := d.ReadAt(context.Background(), "f", -1, 1); err == nil {
		t.Fatal("expected error on negative offset")
	}
}

// --- Open (file:// URI) ---

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

// --- Stat / List ---

func TestStat(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	info, err := d.Stat(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 5 {
		t.Errorf("size: got %d, want 5", info.Size)
	}
	if info.IsDir {
		t.Error("expected file, got dir")
	}
	if info.ModTime.IsZero() {
		t.Error("ModTime not set")
	}
}

func TestList_FiltersHidden(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "dir/visible", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	// Drop a tombstone marker directly to simulate a tombstoned file.
	if err := os.WriteFile(filepath.Join(d.Root(), "dir", "tombstoned.tombstone"), nil, 0o644); err != nil {
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

// --- ListObjectsWithModTime ---

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

func TestListObjectsWithModTime_StopWalk(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := d.Put(ctx, string(rune('a'+i)), strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}

	var seen int
	err := d.ListObjectsWithModTime(ctx, "", time.Time{}, func(m driver.ObjectMeta) error {
		seen++
		if seen == 2 {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fs.SkipAll should be swallowed, got %v", err)
	}
	if seen != 2 {
		t.Fatalf("expected to stop at 2, saw %d", seen)
	}
}

func TestListObjectsWithModTime_MissingPrefixIsEmpty(t *testing.T) {
	d := newTestDriver(t)
	err := d.ListObjectsWithModTime(context.Background(), "nonexistent", time.Time{}, func(m driver.ObjectMeta) error {
		t.Fatalf("callback should not be invoked, got %v", m)
		return nil
	})
	if err != nil {
		t.Fatalf("missing prefix should be empty walk, got %v", err)
	}
}

// --- CountObjects ---

func TestCountObjects(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	for _, p := range []string{"a", "sub/b", "sub/c", "sub/deep/d"} {
		if err := d.Put(ctx, p, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	n, err := d.CountObjects(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("count: got %d, want 4", n)
	}
}

// --- Tombstones ---

func TestMarkTombstone_RenamesExisting(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkTombstone(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Stat(ctx, "f"); !os.IsNotExist(err) {
		t.Fatalf("original still visible: %v", err)
	}
	on, err := d.IsTombstone(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("expected tombstone marker to exist")
	}
}

func TestMarkTombstone_CreatesEmptyMarkerWhenMissing(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.MarkTombstone(ctx, "never_existed"); err != nil {
		t.Fatal(err)
	}
	on, err := d.IsTombstone(ctx, "never_existed")
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("expected marker to be created")
	}
}

func TestMarkTombstone_Idempotent(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := d.MarkTombstone(ctx, "f"); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}

func TestIsTombstone_FalseForUntombstoned(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "f", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	on, err := d.IsTombstone(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Fatal("expected no tombstone")
	}
}

// --- PruneEmptyDirs ---

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

func TestPruneEmptyDirs_MissingRootIsNoOp(t *testing.T) {
	d := newTestDriver(t)
	if err := d.PruneEmptyDirs(context.Background(), "no/such/path"); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

// --- Capabilities ---

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

// --- Clone ---

func TestClone(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "src/file", strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	if err := d.Clone(ctx, "src/file", "dst/copy"); err != nil {
		t.Fatal(err)
	}
	r, err := d.Get(ctx, "dst/copy")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "payload" {
		t.Fatalf("got %q, want %q", got, "payload")
	}
	// Source must be intact.
	if _, err := d.Stat(ctx, "src/file"); err != nil {
		t.Fatalf("source disturbed: %v", err)
	}
}

// --- Context cancellation ---

func TestPut_ContextCancelled(t *testing.T) {
	d := newTestDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Put(ctx, "f", strings.NewReader("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

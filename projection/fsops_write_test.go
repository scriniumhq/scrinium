package projection_test

import (
	"context"
	"errors"
	"io"
	vw "scrinium.dev/projection/view"
	"strings"
	"testing"

	"scrinium.dev/errs"
	"scrinium.dev/internal/testutil/projectionfx"
	"scrinium.dev/projection"
	"scrinium.dev/projection/fsmeta"
)

// --- helpers ---

// newFSOpsForWrite builds an FSOps wired with a FakeSource as
// both ProjectionSource (for the View) and StoreClient (for
// writes). Defaults: namespace=files, scratchDir=t.TempDir(),
// editing=off, quota=unlimited.
func newFSOpsForWrite(t *testing.T, opts ...projection.FSOpsOption) (*projection.FSOps, *projectionfx.FakeSource) {
	t.Helper()
	src := projectionfx.New()
	v, err := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	defaults := []projection.FSOpsOption{
		projection.WithStore(src),
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()),
	}
	o, err := projection.NewFSOps(v, append(defaults, opts...)...)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}
	return o, src
}

// writeAll writes the full byte slice via repeated WriteAt at
// increasing offsets.
func writeAll(t *testing.T, f projection.File, data []byte) {
	t.Helper()
	n, err := f.WriteAt(data, 0)
	if err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if n != len(data) {
		t.Fatalf("WriteAt: short write %d/%d", n, len(data))
	}
}

// --- Create + Write + Close ---

func TestCreate_WritesNewFile(t *testing.T) {
	o, src := newFSOpsForWrite(t)

	f, err := o.Create(context.Background(), "photos/img.jpg", 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeAll(t, f, []byte("hello"))
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Stat sees the new file.
	fi, err := o.Stat("photos/img.jpg")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size != 5 {
		t.Errorf("Size: got %d, want 5", fi.Size)
	}
	if fi.IsDir {
		t.Errorf("expected file, got dir")
	}
	if fi.Mode != 0o644 {
		t.Errorf("Mode: got %#o, want 0644", fi.Mode)
	}

	// Underlying store has it.
	if len(src.Manifests()) != 1 {
		t.Errorf("expected 1 manifest in store, got %d", len(src.Manifests()))
	}
}

func TestCreate_EmptyFile_NoPut(t *testing.T) {
	// Create + Close without WriteAt: scratch deleted, no Put.
	o, src := newFSOpsForWrite(t)

	f, err := o.Create(context.Background(), "empty.txt", 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(src.Manifests()); got != 0 {
		t.Errorf("no Put expected, got %d manifests", got)
	}
	if _, err := o.Stat("empty.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("Stat: expected ErrPathNotFound, got %v", err)
	}
}

func TestCreate_OnExistingPath_Fails(t *testing.T) {
	o, _ := newFSOpsForWrite(t)

	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	_, err := o.Create(context.Background(), "a.txt", 0o644)
	if !errors.Is(err, errs.ErrPathExists) {
		t.Errorf("expected ErrPathExists, got %v", err)
	}
}

func TestCreate_InvalidPath(t *testing.T) {
	o, _ := newFSOpsForWrite(t)

	_, err := o.Create(context.Background(), "/abs/path", 0o644)
	if !errors.Is(err, errs.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
}

func TestCreate_NoNamespace(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := projection.NewFSOps(v,
		projection.WithStore(src),
		projection.WithScratchDir(t.TempDir()))

	_, err := o.Create(context.Background(), "a.txt", 0o644)
	if err == nil || !strings.Contains(err.Error(), "WithNamespace") {
		t.Errorf("expected namespace error, got %v", err)
	}
}

func TestCreate_NoStore(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := projection.NewFSOps(v,
		projection.WithNamespace("files"),
		projection.WithScratchDir(t.TempDir()))

	_, err := o.Create(context.Background(), "a.txt", 0o644)
	if err == nil || !strings.Contains(err.Error(), "WithStore") {
		t.Errorf("expected store error, got %v", err)
	}
}

func TestCreate_OnReadOnly(t *testing.T) {
	o, _ := newFSOpsForWrite(t, projection.WithReadOnly())

	_, err := o.Create(context.Background(), "a.txt", 0o644)
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

func TestCreate_WriteAt_RandomAccess(t *testing.T) {
	o, _ := newFSOpsForWrite(t)

	f, err := o.Create(context.Background(), "a.txt", 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write "world" at offset 6, then "hello," at 0.
	if _, err := f.WriteAt([]byte("world"), 6); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if _, err := f.WriteAt([]byte("hello,"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// Position 5 has the byte from before WriteAt at 6 — actually
	// it's a hole filled with zero by the OS. We accept either 0
	// or space; tests only verify size.
	f.Close()

	fi, _ := o.Stat("a.txt")
	if fi.Size != 11 {
		t.Errorf("Size: got %d, want 11 (hello,?world)", fi.Size)
	}
}

// --- Quota ---

func TestQuota_BlocksWriteOverLimit(t *testing.T) {
	o, _ := newFSOpsForWrite(t, projection.WithScratchQuota(10))

	f, _ := o.Create(context.Background(), "big.bin", 0o644)
	defer f.Close()

	// 9 bytes — under quota.
	if _, err := f.WriteAt(make([]byte, 9), 0); err != nil {
		t.Errorf("9-byte write should pass: %v", err)
	}
	// 2 more bytes pushes total to 11 > quota.
	_, err := f.WriteAt(make([]byte, 2), 9)
	if !errors.Is(err, errs.ErrScratchQuota) {
		t.Errorf("expected ErrScratchQuota, got %v", err)
	}
}

func TestQuota_ReleasedOnClose(t *testing.T) {
	o, _ := newFSOpsForWrite(t, projection.WithScratchQuota(10))

	// First file uses 8 bytes, then closes — quota goes back to
	// zero. Second file can use 8 again.
	f1, _ := o.Create(context.Background(), "a", 0o644)
	if _, err := f1.WriteAt(make([]byte, 8), 0); err != nil {
		t.Fatalf("first WriteAt: %v", err)
	}
	f1.Close()

	f2, _ := o.Create(context.Background(), "b", 0o644)
	if _, err := f2.WriteAt(make([]byte, 8), 0); err != nil {
		t.Errorf("after close, quota should be free: %v", err)
	}
	f2.Close()
}

func TestQuota_Unlimited(t *testing.T) {
	o, _ := newFSOpsForWrite(t) // quota=0 default = unlimited

	f, _ := o.Create(context.Background(), "huge", 0o644)
	defer f.Close()
	// 1 MiB — should pass with no quota cap.
	if _, err := f.WriteAt(make([]byte, 1<<20), 0); err != nil {
		t.Errorf("unlimited quota: %v", err)
	}
}

// --- Unlink ---

func TestUnlink_ExistingFile(t *testing.T) {
	o, src := newFSOpsForWrite(t)

	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	if err := o.Unlink(context.Background(), "a.txt"); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if _, err := o.Stat("a.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("Stat after unlink: expected ErrPathNotFound, got %v", err)
	}
	if got := len(src.Manifests()); got != 0 {
		t.Errorf("store should be empty, got %d manifests", got)
	}
}

func TestUnlink_NotFound(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	err := o.Unlink(context.Background(), "nope")
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestUnlink_OnDirectory(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	f, _ := o.Create(context.Background(), "dir/a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Unlink(context.Background(), "dir")
	if !errors.Is(err, errs.ErrIsADirectory) {
		t.Errorf("expected ErrIsADirectory, got %v", err)
	}
}

func TestUnlink_ReadOnly(t *testing.T) {
	o, _ := newFSOpsForWrite(t, projection.WithReadOnly())
	err := o.Unlink(context.Background(), "anything")
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

// --- Mkdir / Rmdir ---

func TestMkdir_VirtualDirectoryAppearsInStat(t *testing.T) {
	o, _ := newFSOpsForWrite(t)

	if err := o.Mkdir("photos", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	fi, err := o.Stat("photos")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir {
		t.Errorf("expected dir")
	}
}

func TestMkdir_Existing(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	o.Mkdir("photos", 0o755)

	err := o.Mkdir("photos", 0o755)
	if !errors.Is(err, errs.ErrPathExists) {
		t.Errorf("expected ErrPathExists, got %v", err)
	}
}

func TestMkdir_FollowedByCreate(t *testing.T) {
	// mkdir foo + create foo/bar: bar lands in foo, foo becomes a
	// real virtual dir, the pending entry is dropped.
	o, _ := newFSOpsForWrite(t)

	if err := o.Mkdir("photos", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	f, err := o.Create(context.Background(), "photos/img.jpg", 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeAll(t, f, []byte("data"))
	f.Close()

	// photos should still appear as dir (now via View).
	fi, err := o.Stat("photos")
	if err != nil {
		t.Fatalf("Stat photos: %v", err)
	}
	if !fi.IsDir {
		t.Errorf("expected photos to be dir")
	}
	// img.jpg under it.
	fi, err = o.Stat("photos/img.jpg")
	if err != nil {
		t.Fatalf("Stat photos/img.jpg: %v", err)
	}
	if fi.IsDir {
		t.Errorf("expected file")
	}
}

func TestRmdir_PendingDir(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	o.Mkdir("photos", 0o755)
	if err := o.Rmdir("photos"); err != nil {
		t.Fatalf("Rmdir: %v", err)
	}
	if _, err := o.Stat("photos"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("after Rmdir: expected ErrPathNotFound, got %v", err)
	}
}

func TestRmdir_NotEmpty(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	f, _ := o.Create(context.Background(), "photos/a.jpg", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Rmdir("photos")
	if !errors.Is(err, errs.ErrNotEmpty) {
		t.Errorf("expected ErrNotEmpty, got %v", err)
	}
}

func TestRmdir_NotFound(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	err := o.Rmdir("nope")
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestRmdir_OnFile(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Rmdir("a.txt")
	if !errors.Is(err, errs.ErrNotADirectory) {
		t.Errorf("expected ErrNotADirectory, got %v", err)
	}
}

// --- Listdir with pendingDirs ---

func TestListdir_IncludesPendingChildren(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	if err := o.Mkdir("photos/2024", 0o755); err != nil {
		t.Fatalf("Mkdir 2024: %v", err)
	}
	// Mkdir for "photos" too — implicit ancestor would also be
	// pending, but pendingDirs holds only what was explicitly
	// created.
	if err := o.Mkdir("photos", 0o755); err == nil {
		// photos became a parent of pending 2024, but we stored
		// pendingDirs by exact key; "photos" should still be
		// addable as its own pending.
		t.Logf("Mkdir photos succeeded after 2024")
	}

	// Listing root sees "photos" if it was explicitly Mkdir'ed.
	// We did Mkdir photos above, so:
	var names []string
	for fi, err := range o.Listdir("") {
		if err != nil {
			t.Fatalf("Listdir: %v", err)
		}
		names = append(names, fi.Name)
	}
	found := false
	for _, n := range names {
		if n == "photos" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected photos in listing, got %v", names)
	}
}

// --- Read-after-write through Open ---

func TestOpen_AfterCreate_ReadsContent(t *testing.T) {
	o, _ := newFSOpsForWrite(t)

	f, _ := o.Create(context.Background(), "msg.txt", 0o644)
	writeAll(t, f, []byte("hello world"))
	f.Close()

	rh, err := o.Open(context.Background(), "msg.txt", projection.OpenReadOnly)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rh.Close()

	buf := make([]byte, 11)
	n, err := rh.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 11 || string(buf[:n]) != "hello world" {
		t.Errorf("got %q (n=%d), want hello world", buf[:n], n)
	}
}

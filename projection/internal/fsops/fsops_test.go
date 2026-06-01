package fsops_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"scrinium.dev/contract/projection"
	fso "scrinium.dev/projection/internal/fsops"
	vw "scrinium.dev/projection/internal/view"

	"scrinium.dev/domain/fsmeta"
	"scrinium.dev/errs"
	"scrinium.dev/internal/testutil/projectionfx"
)

// --- Construction ---

func TestNewFSOps_NilView(t *testing.T) {
	_, err := fso.New(nil)
	if err == nil {
		t.Fatal("expected error for nil view")
	}
}

func TestNewFSOps_DefaultsApplied(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src)
	defer v.Close()

	o, err := fso.New(v)
	if err != nil {
		t.Fatalf("NewFSOps: %v", err)
	}
	if o == nil {
		t.Fatal("expected FSOps, got nil")
	}
}

// --- Stat ---

func TestStat_FileInRootView(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"photos/img.jpg"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	fi, err := o.Stat("photos/img.jpg")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir {
		t.Errorf("expected file, got dir")
	}
	if fi.Name != "img.jpg" {
		t.Errorf("Name: got %q, want img.jpg", fi.Name)
	}
}

func TestStat_VirtualDirectory(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"photos/2024/img.jpg"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	fi, err := o.Stat("photos/2024")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !fi.IsDir {
		t.Errorf("expected dir")
	}
	if fi.Mode == 0 {
		t.Errorf("Mode: virtual dir should have non-zero default; got 0")
	}
}

func TestStat_NotFound(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)
	_, err := o.Stat("nope/path")
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestStat_AppliesDefaultMode(t *testing.T) {
	// Artifact with fsmeta.Mode = 0 should get the FSOps default.
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v,
		fso.WithDefaultMode(0o600))

	fi, err := o.Stat("a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode != 0o600 {
		t.Errorf("Mode: got %#o, want 0600", fi.Mode)
	}
}

func TestStat_AppliesDefaultUIDGID(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v,
		fso.WithDefaultUID(1000),
		fso.WithDefaultGID(2000))

	fi, _ := o.Stat("a.txt")
	if fi.UID != 1000 || fi.GID != 2000 {
		t.Errorf("UID/GID: got %d/%d, want 1000/2000", fi.UID, fi.GID)
	}
}

// --- RootView routing ---

func TestStat_RoutesViaRootView_ByArtifact(t *testing.T) {
	// When RootView is by-artifact, Stat takes paths in the
	// by-artifact tree shape directly.
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"photos/img.jpg"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver),
		vw.WithRootView(projection.RootByArtifact))
	defer v.Close()

	o, _ := fso.New(v)

	// "photos/img.jpg" is the by-path key, NOT the by-artifact
	// key. With RootByArtifact it must fail.
	if _, err := o.Stat("photos/img.jpg"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("by-path key should not work in RootByArtifact: %v", err)
	}
	// The artifact's by-artifact path should work.
	if _, err := o.Stat("aa/bb/sha256-aabbccdd"); err != nil {
		t.Errorf("by-artifact key: %v", err)
	}
}

// --- Listdir ---

func TestListdir_ListsFiles(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aaaa1111",
		"photos/a.jpg"), nil)
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-bbbb2222",
		"photos/b.jpg"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	var names []string
	for fi, err := range o.Listdir("photos") {
		if err != nil {
			t.Fatalf("Listdir: %v", err)
		}
		names = append(names, fi.Name)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(names), names)
	}
	if names[0] != "a.jpg" || names[1] != "b.jpg" {
		t.Errorf("expected sorted [a.jpg, b.jpg], got %v", names)
	}
}

func TestListdir_OnFile(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	for _, err := range o.Listdir("a.txt") {
		if !errors.Is(err, errs.ErrNotADirectory) {
			t.Errorf("expected ErrNotADirectory, got %v", err)
		}
		return
	}
	t.Fatal("expected at least one yield with error")
}

func TestListdir_NotFound(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	for _, err := range o.Listdir("nope") {
		if !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("expected ErrPathNotFound, got %v", err)
		}
		return
	}
	t.Fatal("expected at least one yield with error")
}

// --- Open ---

func TestOpen_ReadOnly(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"hello.txt"), []byte("hello"))

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	f, err := o.Open(context.Background(), "hello.txt", fso.OpenReadOnly)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	got, err := io.ReadAll(asReader(f))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestOpen_WriteModesRejectedWithoutPolicy(t *testing.T) {
	// With editing=off (default), every write-side Open mode is
	// refused with ErrEditingDisabled. AllowAppend, AllowSetattr,
	// AllowTruncate are all required by their own paths.
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), []byte("x"))

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	for _, mode := range []fso.OpenMode{
		fso.OpenWriteOnly,
		fso.OpenReadWrite,
		fso.OpenAppend,
	} {
		_, err := o.Open(context.Background(), "a.txt", mode)
		if !errors.Is(err, errs.ErrEditingDisabled) {
			t.Errorf("mode %v: expected ErrEditingDisabled, got %v", mode, err)
		}
	}
}

func TestOpen_OnDirectory(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"d/file.txt"), nil)

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	_, err := o.Open(context.Background(), "d", fso.OpenReadOnly)
	if !errors.Is(err, errs.ErrIsADirectory) {
		t.Errorf("expected ErrIsADirectory, got %v", err)
	}
}

func TestOpen_NotFound(t *testing.T) {
	src := projectionfx.New()
	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	_, err := o.Open(context.Background(), "nope", fso.OpenReadOnly)
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

// --- Read-only handle behaviour ---

func TestReadOnlyFile_WriteAtRefused(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), []byte("hello"))

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	f, _ := o.Open(context.Background(), "a.txt", fso.OpenReadOnly)
	defer f.Close()

	_, err := f.WriteAt([]byte("x"), 0)
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

func TestReadOnlyFile_TruncateRefused(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), []byte("hello"))

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	f, _ := o.Open(context.Background(), "a.txt", fso.OpenReadOnly)
	defer f.Close()

	if err := f.Truncate(0); !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

func TestReadOnlyFile_ReadAtRandomAccess(t *testing.T) {
	src := projectionfx.New()
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"a.txt"), []byte("0123456789"))

	v, _ := vw.New(context.Background(), src,
		vw.WithPathResolver(fsmeta.Resolver))
	defer v.Close()

	o, _ := fso.New(v)

	f, _ := o.Open(context.Background(), "a.txt", fso.OpenReadOnly)
	defer f.Close()

	buf := make([]byte, 3)
	n, err := f.ReadAt(buf, 4)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 3 || string(buf) != "456" {
		t.Errorf("ReadAt: got %q (n=%d), want 456 (3)", buf[:n], n)
	}
}

// --- helpers ---

// asReader adapts a File handle into an io.Reader by sequentially
// reading from offset 0. Used to test through io.ReadAll.
func asReader(f fso.File) io.Reader {
	return &fileReader{f: f}
}

type fileReader struct {
	f   fso.File
	off int64
}

func (r *fileReader) Read(p []byte) (int, error) {
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	if err == nil && n == 0 {
		err = io.EOF
	}
	return n, err
}

package projection_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/projectionfx"
	"github.com/rkurbatov/scrinium/projection"
)

// All editing tests need an FSOps with the relevant policy bit
// turned on. newEditingFSOps wraps newFSOpsForWrite with
// EditingOn() unless the caller overrides via opts.
func newEditingFSOps(t *testing.T, opts ...projection.FSOpsOption) (*projection.FSOps, *projectionfx.FakeSource) {
	t.Helper()
	defaults := []projection.FSOpsOption{
		projection.WithEditingPolicy(projection.EditingOn()),
	}
	return newFSOpsForWrite(t, append(defaults, opts...)...)
}

// readAllVia reads the whole file through repeated ReadAt calls.
func readAllVia(t *testing.T, f projection.File) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	return string(buf[:n])
}

// --- Rename ---

func TestRename_MovesContent(t *testing.T) {
	o, _ := newEditingFSOps(t)

	f, _ := o.Create(context.Background(), "old.txt", 0o644)
	writeAll(t, f, []byte("hello"))
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := o.Rename(context.Background(), "old.txt", "new.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := o.Stat("old.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("old gone: expected ErrPathNotFound, got %v", err)
	}
	fi, err := o.Stat("new.txt")
	if err != nil {
		t.Fatalf("Stat new.txt: %v", err)
	}
	if fi.Size != 5 {
		t.Errorf("Size: got %d, want 5", fi.Size)
	}

	// Content survives.
	rh, _ := o.Open(context.Background(), "new.txt", projection.OpenReadOnly)
	defer rh.Close()
	if got := readAllVia(t, rh); got != "hello" {
		t.Errorf("content: got %q, want hello", got)
	}
}

func TestRename_WithoutPolicy(t *testing.T) {
	// Default editing=off — Rename should be refused.
	o, _ := newFSOpsForWrite(t) // no EditingOn

	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Rename(context.Background(), "a.txt", "b.txt")
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

func TestRename_NewPathExists(t *testing.T) {
	o, _ := newEditingFSOps(t)

	f1, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f1, []byte("x"))
	f1.Close()
	f2, _ := o.Create(context.Background(), "b.txt", 0o644)
	writeAll(t, f2, []byte("y"))
	f2.Close()

	err := o.Rename(context.Background(), "a.txt", "b.txt")
	if !errors.Is(err, errs.ErrPathExists) {
		t.Errorf("expected ErrPathExists, got %v", err)
	}
}

func TestRename_OldPathNotFound(t *testing.T) {
	o, _ := newEditingFSOps(t)
	err := o.Rename(context.Background(), "nope", "elsewhere")
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestRename_InvalidNewPath(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Rename(context.Background(), "a.txt", "/abs/path")
	if !errors.Is(err, errs.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
}

func TestRename_SamePathIsNoop(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	if err := o.Rename(context.Background(), "a.txt", "a.txt"); err != nil {
		t.Errorf("self-rename should be no-op, got %v", err)
	}
	if _, err := o.Stat("a.txt"); err != nil {
		t.Errorf("Stat: %v", err)
	}
}

func TestRename_PreservesNonPathFsmetaFields(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o600)
	writeAll(t, f, []byte("x"))
	f.Close()

	if err := o.Rename(context.Background(), "a.txt", "b.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	fi, _ := o.Stat("b.txt")
	// Mode preserved through the rename.
	if fi.Mode != 0o600 {
		t.Errorf("Mode: got %#o, want 0600", fi.Mode)
	}
}

// --- Setattr ---

func TestSetattr_ChangesMode(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	newMode := uint32(0o600)
	if err := o.Setattr(context.Background(), "a.txt", projection.Attrs{Mode: &newMode}); err != nil {
		t.Fatalf("Setattr: %v", err)
	}
	fi, _ := o.Stat("a.txt")
	if fi.Mode != 0o600 {
		t.Errorf("Mode: got %#o, want 0600", fi.Mode)
	}
}

func TestSetattr_ChangesUIDGID(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	newUID := uint32(1000)
	newGID := uint32(2000)
	if err := o.Setattr(context.Background(), "a.txt",
		projection.Attrs{UID: &newUID, GID: &newGID}); err != nil {
		t.Fatalf("Setattr: %v", err)
	}
	fi, _ := o.Stat("a.txt")
	if fi.UID != 1000 || fi.GID != 2000 {
		t.Errorf("UID/GID: got %d/%d, want 1000/2000", fi.UID, fi.GID)
	}
}

func TestSetattr_ChangesModTime(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	target := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := o.Setattr(context.Background(), "a.txt",
		projection.Attrs{ModTime: &target}); err != nil {
		t.Fatalf("Setattr: %v", err)
	}
	fi, _ := o.Stat("a.txt")
	if !fi.ModTime.Equal(target) {
		t.Errorf("ModTime: got %v, want %v", fi.ModTime, target)
	}
}

func TestSetattr_PreservesContent(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("hello world"))
	f.Close()

	newMode := uint32(0o600)
	o.Setattr(context.Background(), "a.txt", projection.Attrs{Mode: &newMode})

	rh, _ := o.Open(context.Background(), "a.txt", projection.OpenReadOnly)
	defer rh.Close()
	if got := readAllVia(t, rh); got != "hello world" {
		t.Errorf("content: got %q, want hello world", got)
	}
}

func TestSetattr_WithoutPolicy(t *testing.T) {
	o, _ := newFSOpsForWrite(t) // editing=off
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	newMode := uint32(0o600)
	err := o.Setattr(context.Background(), "a.txt", projection.Attrs{Mode: &newMode})
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

// --- Truncate ---

func TestTruncate_Shrinks(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("hello world"))
	f.Close()

	if err := o.Truncate(context.Background(), "a.txt", 5); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	fi, _ := o.Stat("a.txt")
	if fi.Size != 5 {
		t.Errorf("Size: got %d, want 5", fi.Size)
	}

	rh, _ := o.Open(context.Background(), "a.txt", projection.OpenReadOnly)
	defer rh.Close()
	if got := readAllVia(t, rh); got != "hello" {
		t.Errorf("content: got %q, want hello", got)
	}
}

func TestTruncate_ToZero(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("data"))
	f.Close()

	if err := o.Truncate(context.Background(), "a.txt", 0); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	fi, _ := o.Stat("a.txt")
	if fi.Size != 0 {
		t.Errorf("Size: got %d, want 0", fi.Size)
	}
}

func TestTruncate_Extends(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("ab"))
	f.Close()

	if err := o.Truncate(context.Background(), "a.txt", 5); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	fi, _ := o.Stat("a.txt")
	if fi.Size != 5 {
		t.Errorf("Size: got %d, want 5", fi.Size)
	}
}

func TestTruncate_NegativeSize(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Truncate(context.Background(), "a.txt", -1)
	if err == nil {
		t.Error("expected error for negative size")
	}
}

func TestTruncate_WithoutPolicy(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	err := o.Truncate(context.Background(), "a.txt", 0)
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

// --- Open for editing ---

func TestOpenReadWrite_EditExisting(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("hello"))
	f.Close()

	wf, err := o.Open(context.Background(), "a.txt", projection.OpenReadWrite)
	if err != nil {
		t.Fatalf("Open RDWR: %v", err)
	}
	// Overwrite the first byte.
	if _, err := wf.WriteAt([]byte("H"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := wf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rh, _ := o.Open(context.Background(), "a.txt", projection.OpenReadOnly)
	defer rh.Close()
	if got := readAllVia(t, rh); got != "Hello" {
		t.Errorf("content: got %q, want Hello", got)
	}
}

func TestOpenReadWrite_WithoutPolicy(t *testing.T) {
	o, _ := newFSOpsForWrite(t) // editing=off
	f, _ := o.Create(context.Background(), "a.txt", 0o644)
	writeAll(t, f, []byte("x"))
	f.Close()

	_, err := o.Open(context.Background(), "a.txt", projection.OpenReadWrite)
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

// --- Read-only override ---

func TestEditing_ReadOnlyTrumpsPolicy(t *testing.T) {
	// Even with EditingOn, WithReadOnly forbids every mutation.
	o, _ := newEditingFSOps(t, projection.WithReadOnly())

	if err := o.Rename(context.Background(), "a", "b"); !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("Rename: got %v", err)
	}
	mode := uint32(0o600)
	if err := o.Setattr(context.Background(), "a", projection.Attrs{Mode: &mode}); !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("Setattr: got %v", err)
	}
	if err := o.Truncate(context.Background(), "a", 0); !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("Truncate: got %v", err)
	}
}

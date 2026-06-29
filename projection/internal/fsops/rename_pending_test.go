package fsops_test

import (
	"context"
	"errors"
	"testing"

	"scrinium.dev/errs"
)

// TestRename_PendingDir renames an empty pending directory. It must succeed on
// a default (editing-OFF) Ops: a pending-dir rename is a namespace operation
// gated like Mkdir (read-only only), not by AllowRename.
func TestRename_PendingDir(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	if err := o.Mkdir("photos", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := o.Rename(context.Background(), "photos", "pics"); err != nil {
		t.Fatalf("Rename pending dir: %v", err)
	}

	if _, err := o.Stat("photos"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("old path: expected ErrPathNotFound, got %v", err)
	}
	fi, err := o.Stat("pics")
	if err != nil {
		t.Fatalf("Stat new path: %v", err)
	}
	if !fi.IsDir {
		t.Errorf("new path: expected directory")
	}
}

// TestRename_PendingDir_NestedPending checks that nested pending directories
// move with their parent.
func TestRename_PendingDir_NestedPending(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	if err := o.Mkdir("a/b", 0o755); err != nil {
		t.Fatalf("Mkdir a/b: %v", err)
	}
	if err := o.Mkdir("a/b/c", 0o755); err != nil {
		t.Fatalf("Mkdir a/b/c: %v", err)
	}

	if err := o.Rename(context.Background(), "a/b", "x"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	for _, gone := range []string{"a/b", "a/b/c"} {
		if _, err := o.Stat(gone); !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("Stat %q: expected ErrPathNotFound, got %v", gone, err)
		}
	}
	for _, want := range []string{"x", "x/c"} {
		fi, err := o.Stat(want)
		if err != nil {
			t.Errorf("Stat %q: %v", want, err)
			continue
		}
		if !fi.IsDir {
			t.Errorf("Stat %q: expected directory", want)
		}
	}
}

// TestRename_PendingDir_TargetExists rejects a rename onto a taken path.
func TestRename_PendingDir_TargetExists(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	f, err := o.Create(context.Background(), "taken.txt", 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeAll(t, f, []byte("x"))
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := o.Mkdir("photos", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := o.Rename(context.Background(), "photos", "taken.txt"); !errors.Is(err, errs.ErrPathExists) {
		t.Errorf("expected ErrPathExists, got %v", err)
	}
}

// TestRename_NonEmptyDir_StillRejected confirms the pending path is empty-only:
// a directory that is backed by real children is not pending, so even with
// AllowRename ON it is rejected with ErrIsADirectory (recursive directory
// rename is a separate, flagged feature).
func TestRename_NonEmptyDir_StillRejected(t *testing.T) {
	o, _ := newEditingFSOps(t)
	f, err := o.Create(context.Background(), "photos/a.jpg", 0o644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeAll(t, f, []byte("x"))
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := o.Rename(context.Background(), "photos", "pics"); !errors.Is(err, errs.ErrIsADirectory) {
		t.Errorf("expected ErrIsADirectory, got %v", err)
	}
}

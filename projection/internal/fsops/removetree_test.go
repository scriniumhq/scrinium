package fsops_test

import (
	"context"
	"errors"
	"testing"

	fso "scrinium.dev/projection/internal/fsops"

	"scrinium.dev/errs"
)

// seedFile creates path with a one-byte body and commits it (Create + write +
// Close), so it lands in both the store and the View — the precondition every
// RemoveTree case needs.
func seedFile(t *testing.T, o *fso.Ops, path string) {
	t.Helper()
	f, err := o.Create(context.Background(), path, 0o644)
	if err != nil {
		t.Fatalf("Create %q: %v", path, err)
	}
	// Unique body per path: the store is content-addressed, so identical
	// bodies would dedup to one manifest and break the store-count asserts.
	writeAll(t, f, []byte("body:"+path))
	if err := f.Close(); err != nil {
		t.Fatalf("Close %q: %v", path, err)
	}
}

func TestRemoveTree_File(t *testing.T) {
	o, src := newFSOpsForWrite(t)
	seedFile(t, o, "a.txt")

	if err := o.RemoveTree(context.Background(), "a.txt"); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	if _, err := o.Stat("a.txt"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("Stat after RemoveTree: expected ErrPathNotFound, got %v", err)
	}
	if got := len(src.Manifests()); got != 0 {
		t.Errorf("store: expected 0 manifests, got %d", got)
	}
}

func TestRemoveTree_RecursiveDir(t *testing.T) {
	o, src := newFSOpsForWrite(t)
	seedFile(t, o, "photos/a.jpg")
	seedFile(t, o, "photos/sub/b.jpg")
	seedFile(t, o, "photos/sub/c.jpg")
	seedFile(t, o, "keep.txt") // sibling at the root — must survive

	if got := len(src.Manifests()); got != 4 {
		t.Fatalf("setup: expected 4 manifests, got %d", got)
	}

	if err := o.RemoveTree(context.Background(), "photos"); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}

	// The whole subtree — files and the virtual dirs that held them — is gone.
	for _, p := range []string{
		"photos", "photos/a.jpg", "photos/sub", "photos/sub/b.jpg", "photos/sub/c.jpg",
	} {
		if _, err := o.Stat(p); !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("Stat %q after RemoveTree: expected ErrPathNotFound, got %v", p, err)
		}
	}
	if _, err := o.Stat("keep.txt"); err != nil {
		t.Errorf("Stat keep.txt: expected survival, got %v", err)
	}
	if got := len(src.Manifests()); got != 1 {
		t.Errorf("store: expected 1 manifest left (keep.txt), got %d", got)
	}
}

func TestRemoveTree_DropsPendingSubdir(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	seedFile(t, o, "photos/a.jpg")
	if err := o.Mkdir("photos/empty", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Precondition: the pending dir is visible before the delete.
	if _, err := o.Stat("photos/empty"); err != nil {
		t.Fatalf("Stat photos/empty (pending): %v", err)
	}

	if err := o.RemoveTree(context.Background(), "photos"); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	for _, p := range []string{"photos", "photos/a.jpg", "photos/empty"} {
		if _, err := o.Stat(p); !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("Stat %q after RemoveTree: expected ErrPathNotFound, got %v", p, err)
		}
	}
}

func TestRemoveTree_PendingDirOnly(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	if err := o.Mkdir("photos", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := o.RemoveTree(context.Background(), "photos"); err != nil {
		t.Fatalf("RemoveTree on pending dir: %v", err)
	}
	if _, err := o.Stat("photos"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("after RemoveTree: expected ErrPathNotFound, got %v", err)
	}
}

func TestRemoveTree_NotFound(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	if err := o.RemoveTree(context.Background(), "nope"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

// TestRemoveTree_BoundaryNotPrefix guards the path-boundary: removing "photos"
// must not touch a name-prefix neighbour like "photos2". The by-path walk
// navigates the tree structure (not a string prefix), so the neighbour stays.
func TestRemoveTree_BoundaryNotPrefix(t *testing.T) {
	o, _ := newFSOpsForWrite(t)
	seedFile(t, o, "photos/a.jpg")
	seedFile(t, o, "photos2/b.jpg")

	if err := o.RemoveTree(context.Background(), "photos"); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	if _, err := o.Stat("photos/a.jpg"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("photos/a.jpg should be gone, got %v", err)
	}
	if _, err := o.Stat("photos2/b.jpg"); err != nil {
		t.Errorf("photos2/b.jpg must survive RemoveTree(%q), got %v", "photos", err)
	}
}

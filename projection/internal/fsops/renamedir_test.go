package fsops_test

import (
	"context"
	"errors"
	"testing"

	fso "scrinium.dev/projection/internal/fsops"

	"scrinium.dev/errs"
)

// Directory rename — both the empty pending-dir case and the recursive
// non-empty case — is gated by AllowDirRename, separate from file rename's
// AllowRename. newEditingFSOps turns the whole editing surface on (EditingOn
// includes AllowDirRename); newFSOpsForWrite leaves it off. seedFile and
// writeAll come from the sibling write/removetree test files.

// --- Empty pending directory (flag on) ---

func TestRename_PendingDir(t *testing.T) {
	o, _ := newEditingFSOps(t)
	if err := o.Mkdir("src", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := o.Rename(context.Background(), "src", "dst"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	fi, err := o.Stat("dst")
	if err != nil {
		t.Fatalf("Stat dst: %v", err)
	}
	if !fi.IsDir {
		t.Errorf("dst: expected dir")
	}
	if _, err := o.Stat("src"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("src gone: expected ErrPathNotFound, got %v", err)
	}
}

func TestRename_PendingDir_NestedPending(t *testing.T) {
	o, _ := newEditingFSOps(t)
	if err := o.Mkdir("p", 0o755); err != nil {
		t.Fatalf("Mkdir p: %v", err)
	}
	if err := o.Mkdir("p/q", 0o755); err != nil {
		t.Fatalf("Mkdir p/q: %v", err)
	}

	if err := o.Rename(context.Background(), "p", "x"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	for _, dir := range []string{"x", "x/q"} {
		fi, err := o.Stat(dir)
		if err != nil {
			t.Fatalf("Stat %q: %v", dir, err)
		}
		if !fi.IsDir {
			t.Errorf("%q: expected dir", dir)
		}
	}
	for _, gone := range []string{"p", "p/q"} {
		if _, err := o.Stat(gone); !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("%q gone: expected ErrPathNotFound, got %v", gone, err)
		}
	}
}

func TestRename_PendingDir_TargetExists(t *testing.T) {
	o, _ := newEditingFSOps(t)
	if err := o.Mkdir("src", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	seedFile(t, o, "dst") // a real file already occupies the target name

	err := o.Rename(context.Background(), "src", "dst")
	if !errors.Is(err, errs.ErrPathExists) {
		t.Errorf("expected ErrPathExists, got %v", err)
	}
}

// --- Recursive non-empty directory (flag on) ---

func TestRename_DirTree_Recursive(t *testing.T) {
	o, src := newEditingFSOps(t)
	seedFile(t, o, "photos/a.jpg")
	seedFile(t, o, "photos/sub/b.jpg")
	seedFile(t, o, "keep.txt") // sibling at the root — must survive

	before := len(src.Manifests())
	if before != 3 {
		t.Fatalf("setup: expected 3 manifests, got %d", before)
	}

	if err := o.Rename(context.Background(), "photos", "pics"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Files land at the new paths, as files.
	for _, f := range []string{"pics/a.jpg", "pics/sub/b.jpg"} {
		fi, err := o.Stat(f)
		if err != nil {
			t.Fatalf("Stat %q: %v", f, err)
		}
		if fi.IsDir {
			t.Errorf("%q: expected file", f)
		}
	}
	// The whole source subtree is gone.
	for _, gone := range []string{"photos", "photos/a.jpg", "photos/sub", "photos/sub/b.jpg"} {
		if _, err := o.Stat(gone); !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("%q gone: expected ErrPathNotFound, got %v", gone, err)
		}
	}
	// The sibling is untouched.
	if _, err := o.Stat("keep.txt"); err != nil {
		t.Errorf("keep.txt: %v", err)
	}
	// Net manifest count is unchanged: each child is one Put + one Delete.
	if after := len(src.Manifests()); after != before {
		t.Errorf("store: expected %d manifests, got %d", before, after)
	}
	// Content rides along (re-path rewrites only the manifest's vfsmeta.Path).
	rh, err := o.Open(context.Background(), "pics/a.jpg", fso.OpenReadOnly)
	if err != nil {
		t.Fatalf("Open pics/a.jpg: %v", err)
	}
	defer rh.Close()
	if got := readAllVia(t, rh); got != "body:photos/a.jpg" {
		t.Errorf("content: got %q, want %q", got, "body:photos/a.jpg")
	}
}

func TestRename_DirTree_CarriesPending(t *testing.T) {
	o, _ := newEditingFSOps(t)
	seedFile(t, o, "photos/a.jpg") // makes photos a view-backed dir
	if err := o.Mkdir("photos/empty", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := o.Rename(context.Background(), "photos", "pics"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if _, err := o.Stat("pics/a.jpg"); err != nil {
		t.Fatalf("Stat pics/a.jpg: %v", err)
	}
	fi, err := o.Stat("pics/empty")
	if err != nil {
		t.Fatalf("Stat pics/empty: %v", err)
	}
	if !fi.IsDir {
		t.Errorf("pics/empty: expected dir")
	}
	if _, err := o.Stat("photos"); !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("photos gone: expected ErrPathNotFound, got %v", err)
	}
}

func TestRename_DirTree_TargetExists(t *testing.T) {
	o, _ := newEditingFSOps(t)
	seedFile(t, o, "photos/a.jpg")
	seedFile(t, o, "pics/b.jpg") // pics already exists as a view-backed dir

	err := o.Rename(context.Background(), "photos", "pics")
	if !errors.Is(err, errs.ErrPathExists) {
		t.Errorf("expected ErrPathExists, got %v", err)
	}
}

func TestRename_DirTree_IntoOwnSubtree(t *testing.T) {
	o, _ := newEditingFSOps(t)
	seedFile(t, o, "photos/a.jpg")

	err := o.Rename(context.Background(), "photos", "photos/sub")
	if !errors.Is(err, errs.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
}

// --- The flag gates both forms (flag off) ---

func TestRename_PendingDir_RequiresFlag(t *testing.T) {
	o, _ := newFSOpsForWrite(t) // editing off: no AllowDirRename
	if err := o.Mkdir("src", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	err := o.Rename(context.Background(), "src", "dst")
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

func TestRename_DirTree_RequiresFlag(t *testing.T) {
	o, _ := newFSOpsForWrite(t) // editing off: no AllowDirRename
	seedFile(t, o, "photos/a.jpg")

	err := o.Rename(context.Background(), "photos", "pics")
	if !errors.Is(err, errs.ErrEditingDisabled) {
		t.Errorf("expected ErrEditingDisabled, got %v", err)
	}
}

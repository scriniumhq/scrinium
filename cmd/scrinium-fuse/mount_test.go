//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"scrinium.dev/internal/testutil/projectionfx"
	"scrinium.dev/internal/testutil/viewfx"
	"scrinium.dev/projection/vfs"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// newTestRoot builds the FUSE mount root over an in-memory
// FakeSource via the VFS facade. Tests pass pre-populated
// manifests through `manifests` because the View backfills
// synchronously at construction — adding to the source after
// NewView has no effect on the View's trees.
func newTestRoot(t *testing.T, manifests ...domain.Manifest) (*node, *projectionfx.FakeSource) {
	t.Helper()
	v, o, src := viewfx.Stack(t, manifests...)
	fsys := vfs.New(v, o, viewfx.RoutingAll())
	return newRoot(fsys, time.Now()), src
}

// --- errnoFromError (FUSE-specific error translation) ---

func TestErrnoFromError_KnownSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want syscall.Errno
	}{
		{"path not found", fmt.Errorf("ctx: %w", errs.ErrPathNotFound), syscall.ENOENT},
		{"path exists", fmt.Errorf("ctx: %w", errs.ErrPathExists), syscall.EEXIST},
		{"is directory", fmt.Errorf("ctx: %w", errs.ErrIsADirectory), syscall.EISDIR},
		{"not a directory", fmt.Errorf("ctx: %w", errs.ErrNotADirectory), syscall.ENOTDIR},
		{"not empty", fmt.Errorf("ctx: %w", errs.ErrNotEmpty), syscall.ENOTEMPTY},
		{"editing disabled", fmt.Errorf("ctx: %w", errs.ErrEditingDisabled), syscall.EROFS},
		{"scratch quota", fmt.Errorf("ctx: %w", errs.ErrScratchQuota), syscall.ENOSPC},
		{"invalid path", fmt.Errorf("ctx: %w", errs.ErrInvalidPath), syscall.EINVAL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errnoFromError(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestErrnoFromError_NilIsZero(t *testing.T) {
	if errnoFromError(nil) != 0 {
		t.Error("nil error must produce zero errno")
	}
}

func TestErrnoFromError_UnknownIsEIO(t *testing.T) {
	if got := errnoFromError(errors.New("kaboom")); got != syscall.EIO {
		t.Errorf("got %v, want EIO", got)
	}
}

// --- inodeForPath (FUSE-specific inode allocation) ---

func TestInodeForPath_StableForSameInput(t *testing.T) {
	if inodeForPath("by-path/photos/img.jpg") != inodeForPath("by-path/photos/img.jpg") {
		t.Error("not stable for the same path")
	}
}

func TestInodeForPath_DifferentForDifferentInput(t *testing.T) {
	if inodeForPath("by-path/photos/img.jpg") == inodeForPath("by-date/photos/img.jpg") {
		t.Error("collision: same inode for different full paths")
	}
}

func TestInodeForPath_RootIsOne(t *testing.T) {
	if inodeForPath("") != 1 {
		t.Error("root inode must be 1")
	}
}

func TestInodeForPath_AvoidsReservedRange(t *testing.T) {
	for i := 0; i < 100; i++ {
		ino := inodeForPath("by-path/p" + string(rune('a'+i)))
		if ino > 0 && ino < 16 {
			t.Errorf("inode %d in reserved range for input p%c", ino, 'a'+i)
		}
	}
}

// --- node: mount-root attributes ---

func TestNode_RootGetattr(t *testing.T) {
	root, _ := newTestRoot(t)
	out := &fuse.AttrOut{}
	if errno := root.Getattr(context.Background(), nil, out); errno != 0 {
		t.Fatalf("Getattr: %v", errno)
	}
	if out.Mode&fuse.S_IFDIR == 0 {
		t.Errorf("Mode missing S_IFDIR: %#o", out.Mode)
	}
}

// --- node.Lookup: negative path returns without NewInode ---

func TestNode_Lookup_Missing(t *testing.T) {
	root, _ := newTestRoot(t)
	out := &fuse.EntryOut{}
	_, errno := root.Lookup(context.Background(), "nope", out)
	if errno != syscall.ENOENT {
		t.Errorf("expected ENOENT, got %v", errno)
	}
}

// --- node.Readdir: surfaces the VFS listing as fuse.DirEntry ---
//
// This verifies the FUSE-side delegation/translation end to end
// (the service-prefix and root-view policy themselves are tested
// at the vfs layer). The artifact "alpha" and the "_scrinium"
// prefix entry must both reach the DirStream.

func TestNode_Readdir_SurfacesEntries(t *testing.T) {
	root, _ := newTestRoot(t,
		projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd", "alpha"))

	stream, errno := root.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir: %v", errno)
	}
	defer stream.Close()

	var hasService, hasAlpha bool
	for stream.HasNext() {
		entry, _ := stream.Next()
		switch entry.Name {
		case "_scrinium":
			hasService = true
		case "alpha":
			hasAlpha = true
		}
	}
	if !hasService {
		t.Error("_scrinium missing from listing")
	}
	if !hasAlpha {
		t.Error("alpha missing from listing")
	}
}

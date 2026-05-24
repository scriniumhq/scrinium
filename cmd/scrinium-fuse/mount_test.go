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
	viewfx "scrinium.dev/internal/testutil/viewfx"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// newTestRoot builds a rootNode wired against an in-memory
// FakeSource. Tests pass any pre-populated manifests through
// `manifests` because the View backfills synchronously at
// construction time — adding to the source after NewView has no
// effect on the View's trees.
func newTestRoot(t *testing.T, manifests ...domain.Manifest) (*rootNode, *projectionfx.FakeSource) {
	t.Helper()
	v, o, src := viewfx.Stack(t, manifests...)
	return &rootNode{
		view:       v,
		fsops:      o,
		store:      src,
		routingCfg: viewfx.RoutingAll(),
		startedAt:  time.Now(),
	}, src
}

// --- errnoFromError ---

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

// --- inodeFor ---

func TestInodeFor_StableForSameInputs(t *testing.T) {
	a := inodeFor("by-path", "photos/img.jpg")
	b := inodeFor("by-path", "photos/img.jpg")
	if a != b {
		t.Errorf("not stable: %d vs %d", a, b)
	}
}

func TestInodeFor_DifferentForDifferentInputs(t *testing.T) {
	a := inodeFor("by-path", "photos/img.jpg")
	b := inodeFor("by-date", "photos/img.jpg")
	if a == b {
		t.Errorf("collision: same inode for different trees: %d", a)
	}
}

func TestInodeFor_RootIsOne(t *testing.T) {
	if inodeFor("", "") != 1 {
		t.Error("root inode must be 1")
	}
}

func TestInodeFor_AvoidsReservedRange(t *testing.T) {
	// Many path/tree combinations should never produce 1..15.
	for i := 0; i < 100; i++ {
		ino := inodeFor("by-path", "p"+string(rune('a'+i)))
		if ino > 0 && ino < 16 {
			t.Errorf("inode %d in reserved range for input p%c", ino, 'a'+i)
		}
	}
}

// --- rootNode.Getattr ---

func TestRootNode_Getattr(t *testing.T) {
	root, _ := newTestRoot(t)
	out := &fuse.AttrOut{}
	if errno := root.Getattr(context.Background(), nil, out); errno != 0 {
		t.Fatalf("Getattr: %v", errno)
	}
	if out.Mode&fuse.S_IFDIR == 0 {
		t.Errorf("Mode missing S_IFDIR: %#o", out.Mode)
	}
}

// --- rootNode.Lookup ---

// Lookup tests that need NewInode to succeed require a real
// FUSE mount (the embedded fs.Inode of rootNode acquires its
// rawBridge only when fs.Mount runs). Those go in
// mount_root_test.go (5c, integration). Here we exercise only
// the negative path where Lookup can return without ever calling
// NewInode.

func TestRootNode_Lookup_Missing(t *testing.T) {
	root, _ := newTestRoot(t)
	out := &fuse.EntryOut{}
	_, errno := root.Lookup(context.Background(), "nope", out)
	if errno != syscall.ENOENT {
		t.Errorf("expected ENOENT, got %v", errno)
	}
}

// --- rootNode.Readdir ---

func TestRootNode_Readdir_IncludesServicePrefix(t *testing.T) {
	root, _ := newTestRoot(t,
		projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd", "alpha"))

	stream, errno := root.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir: %v", errno)
	}
	defer stream.Close()

	var names []string
	for stream.HasNext() {
		entry, _ := stream.Next()
		names = append(names, entry.Name)
	}

	hasService := false
	hasAlpha := false
	for _, n := range names {
		if n == "_scrinium" {
			hasService = true
		}
		if n == "alpha" {
			hasAlpha = true
		}
	}
	if !hasService {
		t.Errorf("_scrinium missing from listing: %v", names)
	}
	if !hasAlpha {
		t.Errorf("alpha missing from listing: %v", names)
	}
}

func TestRootNode_Readdir_ServicePrefixDisabled(t *testing.T) {
	root, _ := newTestRoot(t)
	root.routingCfg.ServicePrefix = "" // disable

	stream, errno := root.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir: %v", errno)
	}
	defer stream.Close()

	for stream.HasNext() {
		entry, _ := stream.Next()
		if entry.Name == "_scrinium" {
			t.Errorf("_scrinium should not appear when servicePrefix is empty")
		}
	}
}

// --- serviceRootNode children ---

func TestServiceRootNode_ChildrenRespectShowFlags(t *testing.T) {
	root, _ := newTestRoot(t)
	s := &serviceRootNode{root: root}
	got := s.children()

	want := map[string]bool{
		"stats": true, "by-artifact": true, "by-date": true,
		"by-session": true, "by-namespace": true, "orphaned": true,
		"by-path": true,
	}
	if len(got) != len(want) {
		t.Errorf("children count: got %d, want %d (%v)", len(got), len(want), got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected child %q", n)
		}
	}
}

func TestServiceRootNode_ChildrenSkipDisabled(t *testing.T) {
	root, _ := newTestRoot(t)
	root.routingCfg.ShowBySession = false
	root.routingCfg.ShowByDate = false
	s := &serviceRootNode{root: root}
	got := s.children()
	for _, n := range got {
		if n == "by-session" || n == "by-date" {
			t.Errorf("disabled tree %q in listing", n)
		}
	}
}

// --- Stats body ---

func TestStatsBody_NonEmpty(t *testing.T) {
	root, src := newTestRoot(t)
	src.Add(projectionfx.ManifestWithFsmetaPath("sha256-aabbccdd",
		"x"), nil)
	body := root.statsBody()
	if len(body) == 0 {
		t.Error("stats body empty")
	}
	// Sanity: must mention TotalNodes.
	if !contains(string(body), "TotalNodes") {
		t.Errorf("missing TotalNodes in body:\n%s", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

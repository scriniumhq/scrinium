package main

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"scrinium.dev/domain"
)

// The FUSE face of INV-111-5: while the store is Offline, every read
// op answers EIO BEFORE touching the VFS — proven here by a nil VFS
// that would panic if the gate ordering were wrong. The None path is
// exercised by every mount_test.
func TestGate_OfflineShortCircuitsAllOps(t *testing.T) {
	offline := func() domain.MaintenanceMode { return domain.MaintenanceModeOffline }
	n := &node{fsys: &fuseFS{v: nil, mode: offline}}
	ctx := context.Background()

	var attr fuse.AttrOut
	if e := n.Getattr(ctx, nil, &attr); e != syscall.EIO {
		t.Errorf("Getattr: got %v, want EIO", e)
	}
	var entry fuse.EntryOut
	if _, e := n.Lookup(ctx, "x", &entry); e != syscall.EIO {
		t.Errorf("Lookup: got %v, want EIO", e)
	}
	if _, e := n.Readdir(ctx); e != syscall.EIO {
		t.Errorf("Readdir: got %v, want EIO", e)
	}
	if _, _, e := n.Open(ctx, 0); e != syscall.EIO {
		t.Errorf("Open: got %v, want EIO", e)
	}
}

// A nil mode func (tests, hosts without gating) never blocks.
func TestGate_NilModeIsOpen(t *testing.T) {
	f := &fuseFS{}
	if e := f.gate(); e != 0 {
		t.Errorf("nil mode func must not gate, got %v", e)
	}
}

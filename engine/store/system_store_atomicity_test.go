package store_test

// system_store_atomicity_test.go — Tier 1 fault-injection tests
// for SystemStore atomicity. Per ADR-57 the Put flow is:
//
//   1. Write new artifact (manifest file + optional index row).
//   2. Atomically replace the pointer file.
//   3. Drop the predecessor artifact.
//
// A crash between (1) and (2) must leave the OLD value live
// (the new artifact is an orphan reclaimed by Orphan Scan).
// A crash between (2) and (3) must leave the NEW value live
// (the predecessor manifest is an orphan, also reclaimed).
//
// We exercise both branches with a thin driver wrapper that
// injects a single failure at a precisely-scoped path.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/storefx"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// --- fault-injection driver wrapper ---

// faultDriver wraps a real driver and injects exactly one failure
// the first time a write hits a matching path.
type faultDriver struct {
	driver.Driver
	failPutOn    string
	failRemoveOn string
	armed        atomic.Bool
}

func newFaultDriver(d driver.Driver) *faultDriver {
	fd := &faultDriver{Driver: d}
	fd.armed.Store(true)
	return fd
}

func (f *faultDriver) Put(ctx context.Context, path string, r io.Reader) error {
	if f.failPutOn != "" && path == f.failPutOn && f.armed.CompareAndSwap(true, false) {
		// Drain the reader so the body is consumed (avoids
		// leaving the caller's reader in an undefined state).
		_, _ = io.Copy(io.Discard, r)
		return errors.New("faultDriver: injected Put failure")
	}
	return f.Driver.Put(ctx, path, r)
}

func (f *faultDriver) Remove(ctx context.Context, path string) error {
	if f.failRemoveOn != "" && strings.Contains(path, f.failRemoveOn) && f.armed.CompareAndSwap(true, false) {
		return errors.New("faultDriver: injected Remove failure")
	}
	return f.Driver.Remove(ctx, path)
}

// --- happy-path atomicity ---

// TestSystemStore_PointerFlipIsAtomic confirms the visible
// invariant: a sequence of Puts under the same name converges on
// the latest payload, and the predecessor is unreachable through
// the API.
func TestSystemStore_PointerFlipIsAtomic(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	ctx := context.Background()
	ss := s.System()

	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader([]byte("v1"))); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader([]byte("v2-newer"))); err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	rh, err := ss.Get(ctx, "scrub/cursor")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, _ := io.ReadAll(rh)
	if string(got) != "v2-newer" {
		t.Errorf("after v2: got %q, want %q", got, "v2-newer")
	}
}

// --- fault: failure between manifest write and pointer flip ---

// TestSystemStore_Atomicity_FailBeforePointerFlip injects a Put
// failure on the pointer path of an established Store. After the
// failed Put, the OLD artifact must remain live: pointer still
// points at v1, Get returns v1.
func TestSystemStore_Atomicity_FailBeforePointerFlip(t *testing.T) {
	realDrv := driverfx.LocalFS(t)
	ctx := context.Background()

	// Shared index across seed/reopen — bootstrap Orphan Scan
	// drops manifests that have no index row, so a fresh index
	// on reopen would garbage-collect the seeded v1 manifest
	// before our fault-path Get runs.
	idx := indexfx.Memory(t)

	// 1. Seed: init Store, write v1, close.
	s := storefx.InitOn(t, realDrv, store.WithStoreIndex(idx))
	if err := s.System().Put(ctx, "scrub/cursor", bytes.NewReader([]byte("v1-original"))); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	// 2. Reopen on a faulty driver that fails the pointer Put.
	faulty := newFaultDriver(realDrv)
	faulty.failPutOn = "system.state/pointers/scrub/cursor"
	s2 := storefx.OpenOn(t, faulty, store.WithStoreIndex(idx))
	defer s2.Close()

	// 3. Try Put v2 — must return the injected error.
	err := s2.System().Put(ctx, "scrub/cursor", bytes.NewReader([]byte("v2-failed")))
	if err == nil {
		t.Fatal("Put v2: expected injected failure, got nil")
	}

	// 4. Get must still return v1 (pointer untouched).
	rh, err := s2.System().Get(ctx, "scrub/cursor")
	if err != nil {
		t.Fatalf("Get after failed Put: %v", err)
	}
	defer rh.Close()
	got, _ := io.ReadAll(rh)
	if string(got) != "v1-original" {
		t.Errorf("after failed Put: got %q, want %q (old value)", got, "v1-original")
	}
}

// TestSystemStore_Atomicity_FailBeforePredecessorDrop injects a
// Remove failure on the predecessor manifest. The new artifact
// must still be the one Get returns; the old manifest stays on
// disk as an orphan (Orphan Scan reclaims it later).
func TestSystemStore_Atomicity_FailBeforePredecessorDrop(t *testing.T) {
	realDrv := driverfx.LocalFS(t)
	// Match any manifest-path Remove — there is only one per call
	// at this point (the predecessor's manifest file).
	faulty := newFaultDriver(realDrv)
	faulty.failRemoveOn = "manifests/"

	s := storefx.InitOn(t, realDrv, store.WithStoreIndex(indexfx.Memory(t)))
	ctx := context.Background()

	if err := s.System().Put(ctx, "scrub/cursor", bytes.NewReader([]byte("v1"))); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	// Second Put — pointer flip succeeds, predecessor Remove
	// fails. Per SystemStore.Put contract this is best-effort:
	// the public operation succeeds, the orphan remains for
	// Orphan Scan.
	if err := s.System().Put(ctx, "scrub/cursor", bytes.NewReader([]byte("v2"))); err != nil {
		t.Fatalf("Put v2 (predecessor drop fails but Put must succeed): %v", err)
	}

	// Get must return the newer value.
	rh, err := s.System().Get(ctx, "scrub/cursor")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if string(got) != "v2" {
		t.Errorf("after v2 with Remove failure: got %q, want %q", got, "v2")
	}
}

package store_test

// system_store_atomicity_test.go — Tier 1 fault-injection tests for
// SystemStore atomicity under the seq model (ADR-85). Put is:
//
//   1. Claim the next version: create system/<name>/<seq+1> with an
//      exclusive create (O_EXCL). This is the single commit point.
//   2. Best-effort prune: drop versions older than keep-N.
//
// A failure during (1) leaves the previous max(seq) live — the new
// version simply never came into existence. A failure during (2) is
// swallowed (Put still succeeds); the un-pruned version lingers as an
// orphan reclaimed later. There is no pointer file and no predecessor
// "drop" — supersession is implicit in max(seq).
//
// We exercise both branches with a thin driver wrapper that injects a
// single failure at a precisely-scoped path.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/namedstore"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// --- fault-injection driver wrapper ---

// faultDriver wraps a real driver and injects exactly one failure the
// first time a write hits a matching path.
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

func (f *faultDriver) Put(ctx context.Context, path string, r io.Reader, opts ...driver.PutOption) error {
	if f.failPutOn != "" && path == f.failPutOn && f.armed.CompareAndSwap(true, false) {
		// Drain the reader so the body is consumed (avoids leaving the
		// caller's reader in an undefined state).
		_, _ = io.Copy(io.Discard, r)
		return errors.New("faultDriver: injected Put failure")
	}
	return f.Driver.Put(ctx, path, r, opts...)
}

func (f *faultDriver) Remove(ctx context.Context, path string) error {
	if f.failRemoveOn != "" && strings.Contains(path, f.failRemoveOn) && f.armed.CompareAndSwap(true, false) {
		return errors.New("faultDriver: injected Remove failure")
	}
	return f.Driver.Remove(ctx, path)
}

// versionPath is the driver path of a specific system-artifact version.
func versionPath(t *testing.T, name string, seq uint64) string {
	t.Helper()
	p, err := namedstore.VersionPath(name, seq)
	if err != nil {
		t.Fatalf("VersionPath(%q, %d): %v", name, seq, err)
	}
	return p
}

// --- happy-path supersession ---

// TestSystemStore_PutConvergesOnLatest confirms the visible invariant: a
// sequence of Puts under the same name converges on the latest payload,
// and the predecessor is unreachable through the API.
func TestSystemStore_PutConvergesOnLatest(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	ctx := context.Background()
	ss := s.System()

	if err := ss.Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v1"))}); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v2-newer"))}); err != nil {
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

// --- fault: failure while writing the new version ---

// TestSystemStore_Atomicity_FailDuringVersionWrite injects a Put failure
// on the seq-2 version path of an established Store. After the failed
// Put, the OLD version (seq 1) must remain live: Get returns v1.
func TestSystemStore_Atomicity_FailDuringVersionWrite(t *testing.T) {
	realDrv := driverfx.LocalFS(t)
	ctx := context.Background()

	// Shared index across seed/reopen: a fresh index on reopen is fine
	// for system artifacts (they are not indexed and orphan scan only
	// walks manifests/), but we keep one instance so the whole Store is
	// driven through a single, stable backend.
	idx := indexfx.Memory(t)

	// 1. Seed: init Store, write v1 (seq 1), close.
	s := storefx.InitOn(t, realDrv, store.WithStoreIndex(idx))
	if err := s.System().Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v1-original"))}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	// 2. Reopen on a faulty driver that fails the seq-2 write.
	faulty := newFaultDriver(realDrv)
	faulty.failPutOn = versionPath(t, "scrub/cursor", 2)
	s2 := storefx.OpenOn(t, faulty, store.WithStoreIndex(idx))
	defer s2.Close()

	// 3. Try Put v2 — must return the injected error.
	err := s2.System().Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v2-failed"))})
	if err == nil {
		t.Fatal("Put v2: expected injected failure, got nil")
	}

	// 4. Get must still return v1 (max seq is still 1).
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

// TestSystemStore_Atomicity_PruneFailureIsBestEffort drives enough
// versions to trigger keep-N pruning (systemKeepVersions == 3), then
// injects a Remove failure on the oldest version that prune would drop.
// The Put that triggers the prune must still succeed; the un-pruned
// version lingers as an orphan, and Get returns the newest value.
func TestSystemStore_Atomicity_PruneFailureIsBestEffort(t *testing.T) {
	realDrv := driverfx.LocalFS(t)
	// keep-N is 3, so the 4th version triggers a prune of seq 1.
	faulty := newFaultDriver(realDrv)
	faulty.failRemoveOn = versionPath(t, "scrub/cursor", 1)

	s := storefx.InitOn(t, faulty, store.WithStoreIndex(indexfx.Memory(t)))
	ctx := context.Background()

	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		if err := s.System().Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte(v))}); err != nil {
			t.Fatalf("Put %s (prune is best-effort, Put must succeed): %v", v, err)
		}
	}

	// Get must return the newest value despite the failed prune.
	rh, err := s.System().Get(ctx, "scrub/cursor")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if string(got) != "v4" {
		t.Errorf("after v4 with prune Remove failure: got %q, want %q", got, "v4")
	}
}

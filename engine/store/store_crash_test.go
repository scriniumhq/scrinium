package store_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/faulty"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// Crash-consistency: a Put interrupted at any single I/O write must
// leave the store in a consistent state after recovery. The atomicity
// claim the engine makes is observable through the public API: after
// reopen, the artifact is either fully present and byte-identical, or
// fully absent — never torn (Get returning partial/wrong bytes, or a
// Walk entry that fails to read).
//
// The sweep arms faulty.SetFailOnCall to fail the k-th blob/manifest
// write of the Put for each k across the operation's write window,
// reopens cleanly against the same backing dir + index, and
// reconciles. This is the property that makes the engine trustworthy
// under power loss, and it generalises every hand-written "interrupted
// write" example into one parametric sweep.

// crashEnv bundles the reusable backing for one sweep iteration: a real
// localfs dir, a faulty wrapper over it, and a shared index. The same
// (inner, idx) pair is reopened after the fault.
type crashEnv struct {
	inner driver.Driver
	fd    *faulty.Driver
	idx   index.StoreIndex
}

func newCrashEnv(t *testing.T) crashEnv {
	t.Helper()
	inner := driverfx.LocalFS(t)
	return crashEnv{
		inner: inner,
		fd:    driverfx.Faulty(t, inner),
		idx:   indexfx.Memory(t),
	}
}

// start initialises a store over the faulty driver. Init runs clean
// (no fault armed yet); the caller arms the fault afterwards.
func (e crashEnv) start(t *testing.T) store.Store {
	t.Helper()
	return storefx.InitOn(t, e.fd, store.WithStoreIndex(e.idx))
}

// reopenClean reopens the same backing dir + index with NO faults, so
// recovery can run unobstructed.
func (e crashEnv) reopenClean(t *testing.T) store.Store {
	t.Helper()
	s, err := store.OpenStore(context.Background(), e.inner,
		store.WithStoreIndex(e.idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	return s
}

func TestCrash_PutTornAtEveryWrite_IsAtomic(t *testing.T) {
	ctx := context.Background()
	ns := "u"
	// A multi-write payload: large enough that Put issues several blob
	// writes, so the sweep covers more than the trivial single-write
	// case.
	payload := bytes.Repeat([]byte("crash-consistency-"), 4096)

	// 1. Measure the Put write window on a clean run.
	window := measurePutWrites(t, payload, ns)
	if window == 0 {
		t.Fatal("measured zero Put writes; cannot sweep")
	}

	// 2. Fail the k-th write of the Put, for every k in the window.
	for k := int64(1); k <= window; k++ {
		k := k
		t.Run(fmt.Sprintf("fail-write-%d", k), func(t *testing.T) {
			env := newCrashEnv(t)
			s := env.start(t)

			// Arm the fault to trip on the k-th Put call AFTER bootstrap.
			base := env.fd.CallCount(faulty.MethodPut)
			env.fd.SetFailOnCall(faulty.MethodPut, base+k)

			id, putErr := s.Put(ctx, mkArtifact(payload), domain.WithNamespace(ns))
			_ = s.Close()

			// 3. Recover and reconcile.
			s2 := env.reopenClean(t)
			defer s2.Close()

			present := walkIDs(t, s2)
			if putErr == nil {
				// The store completed (possibly via internal retry past
				// the one-shot fault): artifact must be present & exact.
				if _, ok := present[id]; !ok {
					t.Fatalf("Put reported success but artifact %s absent after reopen", id)
				}
				if got := getBytes(t, s2, id); !bytes.Equal(got, payload) {
					t.Fatalf("Put succeeded but content torn after reopen")
				}
				return
			}

			// Put failed at write k. After recovery the artifact is
			// either fully gone or fully readable — never torn.
			if len(present) > 1 {
				t.Fatalf("k=%d: %d artifacts visible after a failed Put, want 0 or 1", k, len(present))
			}
			for gotID := range present {
				if got := getBytes(t, s2, gotID); !bytes.Equal(got, payload) {
					t.Fatalf("k=%d: surviving artifact %s is torn (content mismatch)", k, gotID)
				}
			}
		})
	}
}

// measurePutWrites runs one clean Put and returns how many MethodPut
// calls it issued (the write window the sweep iterates over).
func measurePutWrites(t *testing.T, payload []byte, ns string) int64 {
	t.Helper()
	inner := driverfx.LocalFS(t)
	fd := driverfx.Faulty(t, inner)
	s := storefx.InitOn(t, fd, store.WithStoreIndex(indexfx.Memory(t)))
	defer s.Close()

	base := fd.CallCount(faulty.MethodPut)
	if _, err := s.Put(context.Background(), mkArtifact(payload), domain.WithNamespace("ns")); err != nil {
		t.Fatalf("measure Put: %v", err)
	}
	return fd.CallCount(faulty.MethodPut) - base
}

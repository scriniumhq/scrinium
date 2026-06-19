// Tier-1 fault-injection for SystemStore atomicity under the seq model
// (ADR-85). Put is: (1) claim the next version system/<name>/<seq+1> with
// an exclusive create (the single commit point); (2) best-effort prune of
// versions older than keep-N. A failure during (1) leaves the previous
// max(seq) live — the new version simply never existed. A failure during
// (2) is swallowed (Put still succeeds); the un-pruned version lingers as
// an orphan. There is no pointer file and no predecessor "drop" —
// supersession is implicit in max(seq).
//
// Both branches use the shared driverfx.Faulty wrapper (the same
// fault-injection driver the crash sweep uses): SetFailOnCall arms the
// NEXT Put/Remove after the clean setup writes, so the fault lands exactly
// on the version write / prune Remove under test.

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"scrinium.dev/engine/driver/faulty"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/systemstore"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// --- happy-path supersession ---

// TestSystemStore_PutConvergesOnLatest confirms the visible invariant: a
// sequence of Puts under the same name converges on the latest payload,
// and the predecessor is unreachable through the API.
func TestSystemStore_PutConvergesOnLatest(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	ctx := context.Background()
	ss := s.System()

	if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v1"))}); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v2-newer"))}); err != nil {
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

// TestSystemStore_Atomicity_FailDuringVersionWrite injects a Put failure on
// the seq-2 version write of an established Store. After the failed Put, the
// OLD version (seq 1) must remain live: Get returns v1.
func TestSystemStore_Atomicity_FailDuringVersionWrite(t *testing.T) {
	ctx := context.Background()
	inner := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)

	// 1. Seed v1 (seq 1) on a clean store, then close.
	s := storefx.InitOn(t, inner, store.WithStoreIndex(idx))
	if err := s.System().Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v1-original"))}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	// 2. Reopen on a faulty wrapper; arm the NEXT Put (the seq-2 version
	//    write) to fail. Arming after reopen excludes any open-time writes.
	fd := driverfx.Faulty(t, inner)
	s2 := storefx.OpenOn(t, fd, store.WithStoreIndex(idx))
	defer s2.Close()
	fd.SetFailOnCall(faulty.MethodPut, fd.CallCount(faulty.MethodPut)+1)

	// 3. Put v2 — must return the injected error.
	err := s2.System().Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v2-failed"))})
	if !errors.Is(err, errs.ErrInjected) {
		t.Fatalf("Put v2: want injected failure, got %v", err)
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

// TestSystemStore_Atomicity_PruneFailureIsBestEffort drives enough versions
// to trigger keep-N pruning (systemKeepVersions == 3), then injects a Remove
// failure on the oldest version that prune would drop. The Put that triggers
// the prune must still succeed; the un-pruned version lingers as an orphan,
// and Get returns the newest value.
func TestSystemStore_Atomicity_PruneFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	inner := driverfx.LocalFS(t)
	fd := driverfx.Faulty(t, inner)
	s := storefx.InitOn(t, fd, store.WithStoreIndex(indexfx.Memory(t)))
	defer s.Close()

	// keep-N is 3, so the 4th version triggers a prune of seq 1. Write the
	// first three cleanly, then arm the next Remove (the prune) to fail.
	for _, v := range []string{"v1", "v2", "v3"} {
		if err := s.System().Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte(v))}); err != nil {
			t.Fatalf("Put %s: %v", v, err)
		}
	}
	fd.SetFailOnCall(faulty.MethodRemove, fd.CallCount(faulty.MethodRemove)+1)

	// The 4th Put triggers the prune; the prune Remove fails but Put must
	// still succeed (best-effort).
	if err := s.System().Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("v4"))}); err != nil {
		t.Fatalf("Put v4 (prune is best-effort, Put must succeed): %v", err)
	}

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

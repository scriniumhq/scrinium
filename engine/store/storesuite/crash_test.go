// Crash-consistency sweeps. A mutating operation interrupted at any single
// I/O write must leave the store recoverable: after a clean reopen the
// observable state is consistent — never torn (a Get returning partial/wrong
// bytes, a Walk entry that fails to read, or a config that is neither the
// old nor the new value).
//
// Each sweep measures the operation's driver-write window on a clean run,
// then arms faulty.SetFailOnCall to fail the k-th write for every k across
// that window, reopens cleanly against the same backing dir + index (so
// recovery runs unobstructed), and asserts the operation's crash contract.
// This generalises every hand-written "interrupted write" example into one
// parametric sweep and is the property that makes the engine trustworthy
// under power loss.

package storesuite

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/faulty"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

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

// putSessionBytes puts a payload under a session and returns its id.
func putSessionBytes(t *testing.T, s store.Store, sid domain.SessionID, payload []byte) domain.ArtifactID {
	t.Helper()
	id, err := s.Put(context.Background(), artifactfx.PayloadBytes(payload), domain.WithSession(sid))
	if err != nil {
		t.Fatalf("Put(sid=%q): %v", sid, err)
	}
	return id
}

// --- Put: torn at any write is all-or-nothing ---

func TestCrash_PutTornAtEveryWrite_IsAtomic(t *testing.T) {
	ctx := context.Background()
	// A multi-write payload: large enough that Put issues several blob
	// writes, so the sweep covers more than the trivial single-write case.
	payload := bytes.Repeat([]byte("crash-consistency-"), 4096)

	window := measurePutWrites(t, payload)
	if window == 0 {
		t.Fatal("measured zero Put writes; cannot sweep")
	}

	for k := int64(1); k <= window; k++ {
		k := k
		t.Run(fmt.Sprintf("fail-write-%d", k), func(t *testing.T) {
			env := newCrashEnv(t)
			s := env.start(t)

			base := env.fd.CallCount(faulty.MethodPut)
			env.fd.SetFailOnCall(faulty.MethodPut, base+k)

			id, putErr := s.Put(ctx, artifactfx.PayloadBytes(payload))
			_ = s.Close()

			s2 := env.reopenClean(t)
			defer s2.Close()

			present := storekit.WalkIDs(t, s2)
			if putErr == nil {
				// The store completed (possibly via internal retry past the
				// one-shot fault): artifact must be present & exact.
				if _, ok := present[id]; !ok {
					t.Fatalf("Put reported success but artifact %s absent after reopen", id)
				}
				if got := storekit.GetBytes(t, s2, id); !bytes.Equal(got, payload) {
					t.Fatalf("Put succeeded but content torn after reopen")
				}
				return
			}

			// Put failed at write k. After recovery the artifact is either
			// fully gone or fully readable — never torn.
			if len(present) > 1 {
				t.Fatalf("k=%d: %d artifacts visible after a failed Put, want 0 or 1", k, len(present))
			}
			for gotID := range present {
				if got := storekit.GetBytes(t, s2, gotID); !bytes.Equal(got, payload) {
					t.Fatalf("k=%d: surviving artifact %s is torn (content mismatch)", k, gotID)
				}
			}
		})
	}
}

// measurePutWrites runs one clean Put and returns how many MethodPut
// calls it issued (the write window the sweep iterates over).
func measurePutWrites(t *testing.T, payload []byte) int64 {
	t.Helper()
	inner := driverfx.LocalFS(t)
	fd := driverfx.Faulty(t, inner)
	s := storefx.InitOn(t, fd, store.WithStoreIndex(indexfx.Memory(t)))
	defer s.Close()

	base := fd.CallCount(faulty.MethodPut)
	if _, err := s.Put(context.Background(), artifactfx.PayloadBytes(payload)); err != nil {
		t.Fatalf("measure Put: %v", err)
	}
	return fd.CallCount(faulty.MethodPut) - base
}

// --- Delete: torn at any write is all-or-nothing ---

// TestCrash_DeleteTornAtEveryWrite_IsAtomic: a Delete interrupted at any
// Remove leaves the artifact, after recovery, either fully present and
// byte-identical or fully absent — never torn, and never leaking a second
// visible artifact.
func TestCrash_DeleteTornAtEveryWrite_IsAtomic(t *testing.T) {
	ctx := context.Background()
	payload := bytes.Repeat([]byte("delete-crash-"), 2048)

	window := measureDeleteRemoves(t, payload)
	if window == 0 {
		t.Skip("Delete issued no Remove calls; nothing to sweep")
	}

	for k := int64(1); k <= window; k++ {
		k := k
		t.Run(fmt.Sprintf("fail-remove-%d", k), func(t *testing.T) {
			env := newCrashEnv(t)
			s := env.start(t)

			id, err := s.Put(ctx, artifactfx.PayloadBytes(payload)) // clean seed
			if err != nil {
				t.Fatalf("seed Put: %v", err)
			}

			base := env.fd.CallCount(faulty.MethodRemove)
			env.fd.SetFailOnCall(faulty.MethodRemove, base+k)

			delErr := s.Delete(ctx, id)
			_ = s.Close()

			s2 := env.reopenClean(t)
			defer s2.Close()

			present := storekit.WalkIDs(t, s2)
			if delErr == nil {
				// Delete reported success: artifact must be gone.
				if _, ok := present[id]; ok {
					t.Fatalf("Delete succeeded but artifact %s still present after reopen", id)
				}
				return
			}

			// Delete failed at remove k: fully present (exact) or fully gone.
			if _, ok := present[id]; ok {
				if got := storekit.GetBytes(t, s2, id); !bytes.Equal(got, payload) {
					t.Fatalf("k=%d: surviving artifact is torn after failed Delete", k)
				}
			}
			if len(present) > 1 {
				t.Fatalf("k=%d: %d artifacts visible after a single-artifact Delete, want 0 or 1", k, len(present))
			}
		})
	}
}

// measureDeleteRemoves runs one clean Put+Delete and returns how many
// MethodRemove calls the Delete issued.
func measureDeleteRemoves(t *testing.T, payload []byte) int64 {
	t.Helper()
	inner := driverfx.LocalFS(t)
	fd := driverfx.Faulty(t, inner)
	s := storefx.InitOn(t, fd, store.WithStoreIndex(indexfx.Memory(t)))
	defer s.Close()

	id, err := s.Put(context.Background(), artifactfx.PayloadBytes(payload))
	if err != nil {
		t.Fatalf("measure seed Put: %v", err)
	}
	base := fd.CallCount(faulty.MethodRemove)
	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("measure Delete: %v", err)
	}
	return fd.CallCount(faulty.MethodRemove) - base
}

// --- RollbackSession: torn at any write is resumable ---

// TestCrash_RollbackSessionTornAtEveryWrite_Resumable: a RollbackSession
// interrupted at any Remove leaves no torn survivor, and a clean re-run
// after recovery completes — every artifact written under the session is
// gone. (RollbackSession is resumable, not all-or-nothing: see
// TestRollbackSession_ResumesAfterPartialDelete.)
func TestCrash_RollbackSessionTornAtEveryWrite_Resumable(t *testing.T) {
	ctx := context.Background()
	payloads := []string{"sess-alpha", "sess-bravo", "sess-charlie"}

	window := measureRollbackRemoves(t, payloads)
	if window == 0 {
		t.Skip("RollbackSession issued no Remove calls; nothing to sweep")
	}

	for k := int64(1); k <= window; k++ {
		k := k
		t.Run(fmt.Sprintf("fail-remove-%d", k), func(t *testing.T) {
			env := newCrashEnv(t)
			s := env.start(t)

			ids, want := seedSession(t, s, "imp", payloads) // clean

			base := env.fd.CallCount(faulty.MethodRemove)
			env.fd.SetFailOnCall(faulty.MethodRemove, base+k)

			_ = s.RollbackSession(ctx, "imp") // may fail at write k
			_ = s.Close()

			s2 := env.reopenClean(t)
			defer s2.Close()

			// Any survivor of the torn rollback must be byte-exact.
			present := storekit.WalkIDs(t, s2)
			for _, id := range ids {
				if _, ok := present[id]; ok {
					if got := storekit.GetBytes(t, s2, id); !bytes.Equal(got, want[id]) {
						t.Fatalf("k=%d: surviving session artifact %s is torn", k, id)
					}
				}
			}

			// Resume: a clean re-run must complete and leave nothing.
			if err := s2.RollbackSession(ctx, "imp"); err != nil {
				t.Fatalf("k=%d: resume RollbackSession failed: %v", k, err)
			}
			after := storekit.WalkIDs(t, s2)
			for _, id := range ids {
				if _, ok := after[id]; ok {
					t.Fatalf("k=%d: session artifact %s remains after resumed rollback", k, id)
				}
			}
		})
	}
}

// seedSession puts one artifact per payload under sid and returns their ids
// plus an id→payload map for byte-exactness checks.
func seedSession(t *testing.T, s store.Store, sid domain.SessionID, payloads []string) ([]domain.ArtifactID, map[domain.ArtifactID][]byte) {
	t.Helper()
	ids := make([]domain.ArtifactID, 0, len(payloads))
	want := make(map[domain.ArtifactID][]byte, len(payloads))
	for _, p := range payloads {
		id := putSessionBytes(t, s, sid, []byte(p))
		ids = append(ids, id)
		want[id] = []byte(p)
	}
	return ids, want
}

// measureRollbackRemoves runs one clean session + RollbackSession and
// returns how many MethodRemove calls the rollback issued.
func measureRollbackRemoves(t *testing.T, payloads []string) int64 {
	t.Helper()
	inner := driverfx.LocalFS(t)
	fd := driverfx.Faulty(t, inner)
	s := storefx.InitOn(t, fd, store.WithStoreIndex(indexfx.Memory(t)))
	defer s.Close()

	for _, p := range payloads {
		putSessionBytes(t, s, "measure", []byte(p))
	}
	base := fd.CallCount(faulty.MethodRemove)
	if err := s.RollbackSession(context.Background(), "measure"); err != nil {
		t.Fatalf("measure RollbackSession: %v", err)
	}
	return fd.CallCount(faulty.MethodRemove) - base
}

// --- UpdateConfig: torn at any write leaves a consistent config ---

// TestCrash_UpdateConfigTornAtEveryWrite_ConsistentConfig: an UpdateConfig
// interrupted at any write reopens to a store whose active RetentionPeriod
// is either the old value or the new one — never a corrupt or missing
// config descriptor.
func TestCrash_UpdateConfigTornAtEveryWrite_ConsistentConfig(t *testing.T) {
	ctx := context.Background()
	const oldRet, newRet = 2 * time.Hour, 24 * time.Hour

	window := measureUpdateConfigWrites(t, oldRet, newRet)
	if window == 0 {
		t.Skip("UpdateConfig issued no Put calls; nothing to sweep")
	}

	for k := int64(1); k <= window; k++ {
		k := k
		t.Run(fmt.Sprintf("fail-write-%d", k), func(t *testing.T) {
			env := newCrashEnv(t)
			s := storefx.InitOn(t, env.fd,
				store.WithStoreIndex(env.idx),
				store.WithConfig(domain.StoreConfig{RetentionPeriod: oldRet}),
			)

			base := env.fd.CallCount(faulty.MethodPut)
			env.fd.SetFailOnCall(faulty.MethodPut, base+k)

			_ = s.UpdateConfig(ctx, domain.StoreConfig{RetentionPeriod: newRet})
			_ = s.Close()

			s2 := env.reopenClean(t)
			defer s2.Close()

			if got := s2.Config().RetentionPeriod; got != oldRet && got != newRet {
				t.Fatalf("k=%d: config neither old (%v) nor new (%v) after torn UpdateConfig: %v",
					k, oldRet, newRet, got)
			}
		})
	}
}

// measureUpdateConfigWrites runs one clean UpdateConfig and returns how many
// MethodPut calls it issued.
func measureUpdateConfigWrites(t *testing.T, oldRet, newRet time.Duration) int64 {
	t.Helper()
	inner := driverfx.LocalFS(t)
	fd := driverfx.Faulty(t, inner)
	s := storefx.InitOn(t, fd,
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithConfig(domain.StoreConfig{RetentionPeriod: oldRet}),
	)
	defer s.Close()

	base := fd.CallCount(faulty.MethodPut)
	if err := s.UpdateConfig(context.Background(), domain.StoreConfig{RetentionPeriod: newRet}); err != nil {
		t.Fatalf("measure UpdateConfig: %v", err)
	}
	return fd.CallCount(faulty.MethodPut) - base
}

// --- SetPassphrase: torn at any write never bricks the store ---

// TestCrash_SetPassphraseTornAtEveryWrite_NoLockout: SetPassphrase (plain →
// encrypted) interrupted at any write reopens to a store that still opens —
// either still plain (the change did not commit) or encrypted under the new
// passphrase (it did), never a corrupt/locked-out descriptor. The
// pre-existing artifact stays readable in both outcomes. This is the
// crypto-descriptor counterpart of the UpdateConfig (config-descriptor) sweep.
func TestCrash_SetPassphraseTornAtEveryWrite_NoLockout(t *testing.T) {
	ctx := context.Background()
	payload := bytes.Repeat([]byte("pre-existing-"), 256)

	window := measureSetPassphraseWrites(t, payload)
	if window == 0 {
		t.Skip("SetPassphrase issued no Put calls; nothing to sweep")
	}

	for k := int64(1); k <= window; k++ {
		k := k
		t.Run(fmt.Sprintf("fail-write-%d", k), func(t *testing.T) {
			env := newCrashEnv(t)

			// Seed a plain store with one artifact (clean), then close.
			s := env.start(t)
			id, err := s.Put(ctx, artifactfx.PayloadBytes(payload))
			if err != nil {
				t.Fatalf("seed Put: %v", err)
			}
			_ = s.Close()

			// Reopen on the faulty driver with a new-passphrase provider;
			// arm the next Put to fail, then SetPassphrase.
			s2 := storefx.OpenOn(t, env.fd,
				store.WithStoreIndex(env.idx),
				store.WithPassphrase(storefx.StaticPP("new-pw")),
			)
			base := env.fd.CallCount(faulty.MethodPut)
			env.fd.SetFailOnCall(faulty.MethodPut, base+k)
			_ = s2.SetPassphrase(ctx)
			_ = s2.Close()

			// Reopen clean with NO passphrase: the store must open. Still
			// plain (Unlocked) means the change did not commit; encrypted
			// (Locked) means it did and must accept the new passphrase.
			s3, err := store.OpenStore(ctx, env.inner,
				store.WithStoreIndex(env.idx),
				store.WithHashRegistry(storefx.Hashes()),
			)
			if err != nil {
				t.Fatalf("k=%d: store unopenable after torn SetPassphrase (descriptor corruption/lockout): %v", k, err)
			}
			locked := s3.State() == domain.StateLocked
			if !locked {
				if got := storekit.GetBytes(t, s3, id); !bytes.Equal(got, payload) {
					t.Fatalf("k=%d: artifact torn (uncommitted/plain branch)", k)
				}
				_ = s3.Close()
				return
			}
			_ = s3.Close()

			// Committed → encrypted. Reopen with the new passphrase + autounlock.
			s4, err := store.OpenStore(ctx, env.inner,
				store.WithStoreIndex(env.idx),
				store.WithHashRegistry(storefx.Hashes()),
				store.WithPassphrase(storefx.StaticPP("new-pw")),
				store.WithAutoUnlock(),
			)
			if err != nil {
				t.Fatalf("k=%d: encrypted store rejects the new passphrase after torn SetPassphrase (lockout): %v", k, err)
			}
			defer s4.Close()
			if got := storekit.GetBytes(t, s4, id); !bytes.Equal(got, payload) {
				t.Fatalf("k=%d: artifact torn (committed/encrypted branch)", k)
			}
		})
	}
}

// measureSetPassphraseWrites seeds a plain store, reopens with a new-passphrase
// provider and runs one clean SetPassphrase, returning its MethodPut count.
func measureSetPassphraseWrites(t *testing.T, payload []byte) int64 {
	t.Helper()
	inner := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	s := storefx.InitOn(t, inner, store.WithStoreIndex(idx))
	if _, err := s.Put(context.Background(), artifactfx.PayloadBytes(payload)); err != nil {
		t.Fatalf("measure seed Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("measure seed Close: %v", err)
	}

	fd := driverfx.Faulty(t, inner)
	s2 := storefx.OpenOn(t, fd, store.WithStoreIndex(idx), store.WithPassphrase(storefx.StaticPP("new-pw")))
	defer s2.Close()
	base := fd.CallCount(faulty.MethodPut)
	if err := s2.SetPassphrase(context.Background()); err != nil {
		t.Fatalf("measure SetPassphrase: %v", err)
	}
	return fd.CallCount(faulty.MethodPut) - base
}

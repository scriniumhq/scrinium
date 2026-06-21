// Tests for the per-key RWMutex registry. They cover Get's per-key
// stability and mutual exclusion, reader concurrency, per-key
// independence, the registry's own concurrency safety, and LockAll's
// de-duplication and sorted-order (deadlock-free) acquisition. The
// concurrent cases are written to surface data races under `go test -race`,
// and lock state is asserted with TryLock/TryRLock so the checks are
// deterministic and free of sleeps.
package keyedlock_test

import (
	"sync"
	"testing"
	"time"

	"scrinium.dev/internal/keyedlock"
)

// assertHeld fails if mu can be write-acquired (i.e. it is not currently held).
func assertHeld(t *testing.T, mu *sync.RWMutex, key string) {
	t.Helper()
	if mu.TryLock() {
		mu.Unlock()
		t.Fatalf("expected key %q to be locked, but it was free", key)
	}
}

// assertFree fails if mu cannot be write-acquired (i.e. it is currently held).
func assertFree(t *testing.T, mu *sync.RWMutex, key string) {
	t.Helper()
	if !mu.TryLock() {
		t.Fatalf("expected key %q to be free, but it was locked", key)
	}
	mu.Unlock()
}

func TestGet_StablePerKey(t *testing.T) {
	m := keyedlock.New()
	if a, b := m.Get("k"), m.Get("k"); a != b {
		t.Errorf("Get(%q) was not stable: %p then %p", "k", a, b)
	}
	if a, b := m.Get("x"), m.Get("y"); a == b {
		t.Error("Get returned the same mutex for two distinct keys")
	}
	if m1, m2 := keyedlock.New(), keyedlock.New(); m1.Get("k") == m2.Get("k") {
		t.Error("distinct Maps shared a mutex for the same key")
	}
}

func TestGet_DifferentKeysIndependent(t *testing.T) {
	m := keyedlock.New()
	a := m.Get("a")
	a.Lock()
	defer a.Unlock()
	// Holding "a" must not affect a distinct key.
	assertFree(t, m.Get("b"), "b")
}

func TestGet_ReadersConcurrentWritersExcluded(t *testing.T) {
	m := keyedlock.New()
	mu := m.Get("k")

	mu.RLock() // reader 1
	if !mu.TryRLock() {
		mu.RUnlock()
		t.Fatal("a second reader could not acquire a shared read lock")
	}
	// Two read holds are active; a writer must be excluded.
	if mu.TryLock() {
		mu.Unlock()
		t.Fatal("a writer acquired the lock while readers held it")
	}
	mu.RUnlock() // release reader 2
	mu.RUnlock() // release reader 1

	if !mu.TryLock() {
		t.Fatal("a writer could not acquire the lock after all readers released")
	}
	mu.Unlock()
}

func TestGet_SerialisesSameKey(t *testing.T) {
	m := keyedlock.New()
	const goroutines, perG = 50, 200
	counter := 0 // plain int: a broken lock loses updates (and races under -race)

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perG; j++ {
				mu := m.Get("shared")
				mu.Lock()
				counter++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if want := goroutines * perG; counter != want {
		t.Errorf("lost updates: counter = %d, want %d", counter, want)
	}
}

func TestGet_ConcurrentSameKeyReturnsSameMutex(t *testing.T) {
	m := keyedlock.New()
	const goroutines = 100
	got := make([]*sync.RWMutex, goroutines)

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			got[idx] = m.Get("race")
		}(i)
	}
	close(start)
	wg.Wait()

	first := got[0]
	for i, mu := range got {
		if mu != first {
			t.Fatalf("Get(%q) not stable under concurrency: index %d = %p, want %p", "race", i, mu, first)
		}
	}
}

func TestLockAll_Empty(t *testing.T) {
	m := keyedlock.New()
	unlock := m.LockAll()
	if unlock == nil {
		t.Fatal("LockAll() returned a nil release func")
	}
	unlock() // must be a callable no-op
}

func TestLockAll_LocksAndReleases(t *testing.T) {
	m := keyedlock.New()
	keys := []string{"a", "b", "c"}

	unlock := m.LockAll(keys...)
	for _, k := range keys {
		assertHeld(t, m.Get(k), k)
	}
	unlock()
	for _, k := range keys {
		assertFree(t, m.Get(k), k)
	}
}

func TestLockAll_UsesRegistryLocks(t *testing.T) {
	m := keyedlock.New()
	unlock := m.LockAll("x")
	// LockAll must lock the very mutex Get hands out, not a private copy.
	assertHeld(t, m.Get("x"), "x")
	unlock()
	assertFree(t, m.Get("x"), "x")
}

func TestLockAll_Dedup(t *testing.T) {
	m := keyedlock.New()
	// Passing the same key repeatedly must lock it once; a naive impl would
	// self-deadlock on the second Lock of the same mutex. Run in a separate
	// goroutine so a hang is caught by the timeout instead of blocking the test.
	done := make(chan func(), 1)
	go func() { done <- m.LockAll("dup", "dup", "dup") }()

	select {
	case unlock := <-done:
		assertHeld(t, m.Get("dup"), "dup")
		unlock()
		assertFree(t, m.Get("dup"), "dup")
	case <-time.After(5 * time.Second):
		t.Fatal("LockAll self-deadlocked on duplicate keys (de-dup broken)")
	}
}

func TestLockAll_NoDeadlockOnReversedOrder(t *testing.T) {
	m := keyedlock.New()
	const iters = 5000

	var wg sync.WaitGroup
	wg.Add(2)
	work := func(k1, k2 string) {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			m.LockAll(k1, k2)() // acquire then immediately release
		}
	}
	// Reversed key orders would deadlock (AB-BA) without LockAll's internal sort.
	go work("a", "b")
	go work("b", "a")

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("LockAll deadlocked on reversed key order (sorted acquisition broken)")
	}
}

func TestLockAll_MutualExclusionOnOverlap(t *testing.T) {
	m := keyedlock.New()
	const goroutines, perG = 40, 100
	counter := 0

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		// Each holds a private key plus a shared one; they serialise on "shared".
		go func(idx int) {
			defer wg.Done()
			<-start
			priv := "p" + string(rune('a'+idx%26))
			for j := 0; j < perG; j++ {
				unlock := m.LockAll(priv, "shared")
				counter++
				unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if want := goroutines * perG; counter != want {
		t.Errorf("lost updates under overlapping LockAll: counter = %d, want %d", counter, want)
	}
}

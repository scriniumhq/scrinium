// Tests for the generic, concurrency-safe string-keyed registry Map. They
// cover the three duplicate policies exposed through Set / SetFirstWins,
// zero-value Get, sorted Keys, Len, and concurrent access (single-winner
// SetFirstWins and many distinct keys) intended to run under `go test -race`.
package registry_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"scrinium.dev/internal/registry"
)

func TestNew_Empty(t *testing.T) {
	r := registry.New[int]()
	if r.Len() != 0 {
		t.Errorf("Len = %d, want 0", r.Len())
	}
	if got := r.Keys(); len(got) != 0 {
		t.Errorf("Keys = %v, want empty", got)
	}
	if v, ok := r.Get("absent"); ok || v != 0 {
		t.Errorf("Get(absent) = (%d, %v), want (0, false)", v, ok)
	}
}

func TestSet_LastWins(t *testing.T) {
	r := registry.New[int]()
	r.Set("k", 1)
	if v, ok := r.Get("k"); !ok || v != 1 {
		t.Fatalf("after first Set: Get = (%d, %v), want (1, true)", v, ok)
	}
	r.Set("k", 2) // unconditional overwrite
	if v, ok := r.Get("k"); !ok || v != 2 {
		t.Errorf("after overwrite Set: Get = (%d, %v), want (2, true)", v, ok)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d after overwriting one key, want 1", r.Len())
	}
}

func TestSetFirstWins(t *testing.T) {
	r := registry.New[int]()
	if stored := r.SetFirstWins("k", 1); !stored {
		t.Fatal("SetFirstWins on absent key reported not-stored, want stored")
	}
	if v, ok := r.Get("k"); !ok || v != 1 {
		t.Fatalf("Get = (%d, %v), want (1, true)", v, ok)
	}
	if stored := r.SetFirstWins("k", 2); stored {
		t.Error("SetFirstWins on present key reported stored, want dropped")
	}
	if v, ok := r.Get("k"); !ok || v != 1 {
		t.Errorf("Get = (%d, %v) after dropped SetFirstWins, want (1, true) — first must win", v, ok)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
}

func TestGet_ZeroValueForPointer(t *testing.T) {
	r := registry.New[*int]()
	v, ok := r.Get("absent")
	if ok {
		t.Error("Get(absent) reported present")
	}
	if v != nil {
		t.Error("Get(absent) returned a non-nil zero value for a pointer type")
	}
}

func TestKeys_Sorted(t *testing.T) {
	r := registry.New[int]()
	for _, k := range []string{"delta", "alpha", "charlie", "bravo"} {
		r.Set(k, 0)
	}
	got := r.Keys()
	want := []string{"alpha", "bravo", "charlie", "delta"}
	if !slices.Equal(got, want) {
		t.Errorf("Keys = %v, want %v (sorted)", got, want)
	}
}

func TestLen_CountsDistinctKeys(t *testing.T) {
	r := registry.New[int]()
	r.Set("a", 1)
	r.Set("b", 1)
	r.Set("a", 2) // overwrite — not a new key
	if r.Len() != 2 {
		t.Errorf("Len = %d, want 2", r.Len())
	}
}

func TestSetFirstWins_ConcurrentSingleWinner(t *testing.T) {
	r := registry.New[int]()
	const goroutines = 100
	wins := make([]bool, goroutines) // distinct indices: no shared write

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			<-start
			wins[id] = r.SetFirstWins("contested", id)
		}(i)
	}
	close(start)
	wg.Wait()

	count := 0
	for _, w := range wins {
		if w {
			count++
		}
	}
	if count != 1 {
		t.Errorf("SetFirstWins: %d winners, want exactly 1", count)
	}
	if _, ok := r.Get("contested"); !ok {
		t.Error("contested key absent after concurrent SetFirstWins")
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
}

func TestConcurrent_DistinctKeysAndReaders(t *testing.T) {
	r := registry.New[int]()
	const goroutines = 100

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			<-start
			key := fmt.Sprintf("k%03d", id)
			if !r.SetFirstWins(key, id) {
				t.Errorf("SetFirstWins(%q) reported already-present for a distinct key", key)
			}
			// interleave reads to exercise the read/write lock under -race
			_, _ = r.Get(key)
			_ = r.Keys()
			_ = r.Len()
		}(i)
	}
	close(start)
	wg.Wait()

	if r.Len() != goroutines {
		t.Errorf("Len = %d, want %d", r.Len(), goroutines)
	}
	if got := r.Keys(); len(got) != goroutines {
		t.Errorf("len(Keys) = %d, want %d", len(got), goroutines)
	} else if !slices.IsSorted(got) {
		t.Error("Keys not sorted after concurrent registration")
	}
}

package fsops

import (
	"slices"
	"sync"
)

// pathLockManager is the per-path RWMutex registry used by Ops
// to serialise mutating operations on a path while permitting
// concurrent readers.
//
// The map is never pruned: an Ops instance accumulates one
// lock per unique path touched in its lifetime. For typical
// mount sessions the count stays in the thousands; pruning would
// require reference counting that is not worth the complexity
// at this stage.
type pathLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func newPathLockManager() *pathLockManager {
	return &pathLockManager{
		locks: make(map[string]*sync.RWMutex),
	}
}

// Get returns the RWMutex for path, creating one on first
// access. Stable: the same path always returns the same mutex.
func (m *pathLockManager) Get(path string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[path]
	if !ok {
		l = &sync.RWMutex{}
		m.locks[path] = l
	}
	return l
}

// lockOrdered locks every path in lex order so two concurrent
// callers locking the same set of paths cannot deadlock.
// Returns a single function that releases all of them in
// reverse order.
//
// Used by Rename (4b) which holds two paths simultaneously.
func (m *pathLockManager) lockOrdered(paths ...string) func() {
	if len(paths) == 0 {
		return func() {}
	}
	// Copy so we do not mutate the caller's slice.
	sorted := append([]string(nil), paths...)
	slices.Sort(sorted)
	taken := make([]*sync.RWMutex, 0, len(sorted))
	for _, p := range sorted {
		l := m.Get(p)
		l.Lock()
		taken = append(taken, l)
	}
	return func() {
		for i := len(taken) - 1; i >= 0; i-- {
			taken[i].Unlock()
		}
	}
}

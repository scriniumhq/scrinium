package keyedlock

import (
	"slices"
	"sync"
)

// Map hands out a stable *sync.RWMutex per string key. The zero value is
// not ready for use — construct with New. All methods are safe for
// concurrent use.
type Map struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

// New returns an empty Map ready for use.
func New() *Map {
	return &Map{locks: make(map[string]*sync.RWMutex)}
}

// Get returns the RWMutex for key, creating one on first access. Stable:
// the same key always returns the same mutex, so two callers that look up
// the same key contend on the same lock.
func (m *Map) Get(key string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[key]
	if !ok {
		l = &sync.RWMutex{}
		m.locks[key] = l
	}
	return l
}

// LockAll write-locks every key in sorted order — so two callers locking
// the same set of keys acquire them in the same order and cannot
// deadlock — and returns a single function that releases them all in
// reverse order. Duplicate keys are de-duplicated, so passing the same
// key twice locks it once (and would otherwise self-deadlock). Locking
// no keys returns a no-op release.
//
// The canonical use is an operation that must hold several keys at once,
// such as a rename holding both the source and destination paths.
func (m *Map) LockAll(keys ...string) (unlock func()) {
	if len(keys) == 0 {
		return func() {}
	}
	// Copy (so the caller's slice is untouched), sort, and de-dup.
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)

	taken := make([]*sync.RWMutex, 0, len(sorted))
	for _, k := range sorted {
		l := m.Get(k)
		l.Lock()
		taken = append(taken, l)
	}
	return func() {
		for i := len(taken) - 1; i >= 0; i-- {
			taken[i].Unlock()
		}
	}
}

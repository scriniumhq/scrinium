package registry

import (
	"slices"
	"sync"
)

// Map is a concurrency-safe map from a string key to a value V. The zero
// value is not ready for use — construct with New. Reads (Get, Keys, Len)
// take the read lock; writes (Set, SetFirstWins) take the write lock.
// After the init() registration phase, lookups are read-only and
// uncontended in practice.
type Map[V any] struct {
	mu sync.RWMutex
	m  map[string]V
}

// New returns an empty Map ready for use.
func New[V any]() *Map[V] {
	return &Map[V]{m: make(map[string]V)}
}

// SetFirstWins stores v under key only if key is absent, reporting whether
// it was stored. It is the idempotent-registration policy (ADR-63): the
// first registration wins and later ones are ignored. Callers that treat a
// duplicate as a programming error check the result and panic; callers
// that want idempotent side-effect registration ignore it.
func (r *Map[V]) SetFirstWins(key string, v V) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[key]; exists {
		return false
	}
	r.m[key] = v
	return true
}

// Set stores v under key unconditionally — last write wins.
func (r *Map[V]) Set(key string, v V) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[key] = v
}

// Get returns the value stored under key and whether it was present.
func (r *Map[V]) Get(key string) (V, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[key]
	return v, ok
}

// Keys returns the registered keys in sorted order — deterministic output
// for error messages and --help listings.
func (r *Map[V]) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Len reports the number of registered keys.
func (r *Map[V]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}

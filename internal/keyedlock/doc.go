// Package keyedlock provides a concurrency-safe registry of per-key
// RWMutexes: each distinct key gets its own lock, created on first use
// and stable thereafter, so callers can serialise operations on one key
// while operations on other keys proceed in parallel.
//
// It was lifted out of the projection's path locking (Rename and the
// single-path mutators) so the same primitive is available to any other
// per-name serialisation need (system-artifact / lease names, a future
// per-namespace guard) and can be tested on its own.
//
// The map is never pruned: a Map accumulates one mutex per distinct key
// touched in its lifetime. For the expected key counts (paths in a mount
// session, a bounded set of names) that is fine; pruning would require
// reference counting that is not worth the complexity here.
package keyedlock

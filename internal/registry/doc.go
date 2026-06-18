// Package registry is the engine's shared, concurrency-safe, string-keyed
// store of registered plugin values — driver/index dialers, agent and
// wrapper factories, pipeline transformer factories, hash constructors,
// secret-ref resolvers.
//
// Every one of those subsystems had hand-rolled the same locked map: a
// sync.RWMutex, a map[string]V, a Register that took the write lock, a
// lookup that took the read lock, and a sorted-keys lister for error
// messages and --help output. Map[V] collapses that mechanics into one
// place, leaving each subsystem only its own thin, typed wrapper and its
// own duplicate policy:
//
//   - first-wins / idempotent (driver, index, secretref): SetFirstWins,
//     bool ignored — the first registration wins, later ones are dropped
//     (ADR-63).
//   - duplicate-is-a-bug (agent, wrapper): SetFirstWins, panic on false.
//   - last-wins / chainable (pipeline, hashing): Set.
package registry

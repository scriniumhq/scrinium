// Package dagcheck verifies that the import graph between project
// packages matches the DAG declared in
// docs/2. Internals/01 Topology §1.7.
//
// It runs as a regular `go test` — an insurance policy against
// stray upward imports that are easy to miss during refactoring
// but break the layer isolation.
//
// DAG (import direction: A -> B means A imports B):
//
//	cmd        ─┐
//	             ├─> projection ─┐
//	             │               │
//	             ├─> agent ──────┤
//	             ├─> maintenance─┤
//	             ├─> curator ────┤
//	             │               │
//	             │               ├─> plugin ─┐
//	             │               ├─> index ──┤
//	             │               │           │
//	             │               └──> core <─┘
//	             │                    │
//	             │                    ├─> driver ─> event
//	             │                    └─> event
//	             │
//	             └─> internal/dagcheck (test infrastructure)
//
// Extra rules:
//   - projection does NOT import curator (the dependency is
//     inverted via ProjectionSource).
//   - curator/bundler, curator/chunker, curator/host are subpackages
//     of curator; they may import core and curator. They do not
//     import agent, maintenance, or projection.
package dagcheck

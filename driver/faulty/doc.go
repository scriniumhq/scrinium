// Package faulty wraps another driver.Driver and injects
// configurable faults: errors and latency on a per-method basis.
// Used in chaos tests across higher layers (StoreIndex, Pipeline
// runner, Curator) to verify error-path correctness without
// physically corrupting a real filesystem.
//
// Faults are deterministic given a seed: re-running a test with
// the same WithSeed produces the same fault sequence.
//
// Concurrent calls share one rand.Rand guarded by a mutex; the
// throughput cost is acceptable because the faulty driver is a
// test-only tool, never used in production.
//
// DAG: faulty imports driver and the standard library. It does not
// import core or higher layers.
package faulty

// Package drivertest provides a reusable conformance suite that any
// driver.Driver implementation must pass.
//
// It mirrors engine/index/indexsuite: a Factory supplies fresh Driver
// instances and Run exercises the full black-box contract — Put/Get
// round-trips and atomicity, ReadAt ranges, Remove/Rename/Clone,
// Stat/List/ListObjectsWithModTime/CountObjects, PruneEmptyDirs, and
// the tombstone lifecycle.
//
// Scope: only the backend-independent contract lives here. Anything
// tied to a particular backend — localfs path resolution and on-disk
// layout, the file:// scheme, the rename-based tombstone mechanism, the
// exact Capabilities mask, S3 object semantics — stays in that backend's
// own package tests, where it can assert storage-level invariants the
// contract deliberately leaves open. The faulty driver injecting zero
// faults is expected to pass this suite unchanged, proving it a faithful
// pass-through.
//
// Usage (per backend):
//
//	func TestConformance(t *testing.T) {
//		drivertest.Run(t, drivertest.Factory{
//			Name: "localfs",
//			New: func(t *testing.T) driver.Driver {
//				d, err := New(t.TempDir(), WithFsync(false))
//				if err != nil {
//					t.Fatalf("New: %v", err)
//				}
//				return d
//			},
//		})
//	}
package drivertest

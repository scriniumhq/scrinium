package localfs

import (
	"testing"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/drivertest"
)

// TestConformance runs the shared Driver conformance suite against the
// localfs implementation.
//
// The factory roots each instance in its own t.TempDir() with fsync
// disabled so the suite stays fast; fsync governs durability, not the
// atomicity or correctness the suite checks. Backend-specific behaviour
// (path-safety rejection, the file:// scheme, the rename-based tombstone
// mechanism, the exact Capabilities mask, dotfile/temp filtering) stays
// in localfs_test.go.
func TestConformance(t *testing.T) {
	drivertest.Run(t, drivertest.Factory{
		Name: "localfs",
		New: func(t *testing.T) driver.Driver {
			t.Helper()
			d, err := New(t.TempDir(), WithFsync(false))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return d
		},
	})
}

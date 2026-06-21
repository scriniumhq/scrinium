package drivertest

import (
	"io"
	"strings"
	"testing"

	"scrinium.dev/engine/driver"
)

// Factory describes one Driver implementation under test.
type Factory struct {
	// Name appears in subtest output as a prefix. The suite uses
	// t.Run(Name+"/"+section) so multiple factories can be exercised
	// from the same entry point if ever needed.
	Name string

	// New returns a fresh Driver rooted at an isolated, test-scoped
	// location. Each subtest gets its own instance — implementations
	// should rely on t.Cleanup for teardown and never share state
	// across subtests.
	New func(t *testing.T) driver.Driver
}

// Run executes the full Driver conformance suite against f. It checks
// only the black-box contract every backend must honour; backend-
// specific mechanics (on-disk layout, path resolution, scheme support)
// stay in the per-package tests.
func Run(t *testing.T, f Factory) {
	t.Helper()
	if f.New == nil {
		t.Fatal("drivertest.Run: Factory.New is nil")
	}
	if f.Name == "" {
		f.Name = "anon"
	}

	// One section per interface method (plus the tombstone group). Each
	// is its own t.Run so a failure in one method's tests does not hide
	// failures elsewhere.
	t.Run(f.Name+"/Put", func(t *testing.T) { runPut(t, f) })
	t.Run(f.Name+"/Get", func(t *testing.T) { runGet(t, f) })
	t.Run(f.Name+"/ReadAt", func(t *testing.T) { runReadAt(t, f) })
	t.Run(f.Name+"/Remove", func(t *testing.T) { runRemove(t, f) })
	t.Run(f.Name+"/Rename", func(t *testing.T) { runRename(t, f) })
	t.Run(f.Name+"/Clone", func(t *testing.T) { runClone(t, f) })
	t.Run(f.Name+"/Stat", func(t *testing.T) { runStat(t, f) })
	t.Run(f.Name+"/List", func(t *testing.T) { runList(t, f) })
	t.Run(f.Name+"/ListObjectsWithModTime", func(t *testing.T) { runListObjects(t, f) })
	t.Run(f.Name+"/CountObjects", func(t *testing.T) { runCountObjects(t, f) })
	t.Run(f.Name+"/PruneEmptyDirs", func(t *testing.T) { runPruneEmptyDirs(t, f) })
	t.Run(f.Name+"/Tombstone", func(t *testing.T) { runTombstone(t, f) })
}

// putBlob writes data to key and fails the test on error.
func putBlob(t *testing.T, d driver.Driver, key, data string) {
	t.Helper()
	if err := d.Put(t.Context(), key, strings.NewReader(data)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

// getBlob reads the whole object at key and returns it as a string,
// failing the test on any error.
func getBlob(t *testing.T, d driver.Driver, key string) string {
	t.Helper()
	r, err := d.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll(%q): %v", key, err)
	}
	return string(b)
}

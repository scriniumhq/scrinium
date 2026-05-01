// Package driverfx supplies driver fixtures for tests.
//
// Faulty is intentionally absent: a fixture here would import
// driver/faulty, creating an import cycle for faulty's own
// in-package tests.
package driverfx

import (
	"testing"

	"github.com/rkurbatov/scrinium/driver/localfs"
)

// LocalFS returns a fresh localfs.Driver in t.TempDir() with fsync off.
func LocalFS(t testing.TB) *localfs.Driver {
	t.Helper()
	d, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("driverfx.LocalFS: %v", err)
	}
	return d
}

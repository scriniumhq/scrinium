// Package driverfx supplies driver fixtures for tests across the
// codebase. Centralising the construction here means every test
// gets the same defaults (fsync off, per-test temp dir) without
// each package re-implementing a four-line helper.
//
// Adding a new fixture? Two rules:
//  1. Use t.TempDir() so cleanup is automatic.
//  2. Default to "fast and tolerant" knobs (fsync off, no
//     artificial latency). Tests that need real durability or
//     specific timing pass options explicitly.
//
// Note on faulty: a Faulty fixture is intentionally absent.
// driver/faulty's own tests are in package faulty (white-box,
// in-package) and a fixture here would import driver/faulty,
// creating a cycle. Other consumers of faulty (chaos tests in
// later milestones) build it inline.
package driverfx

import (
	"testing"

	"github.com/rkurbatov/scrinium/driver/localfs"
)

// LocalFS returns a fresh localfs.Driver rooted at a per-test
// temporary directory with fsync disabled. The temp dir is removed
// automatically when the test ends.
//
// Use this whenever a test needs *any* driver and does not care
// about the specific backend — most tests do not.
func LocalFS(t *testing.T) *localfs.Driver {
	t.Helper()
	d, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("driverfx.LocalFS: %v", err)
	}
	return d
}

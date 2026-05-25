package driverfx

import (
	"testing"

	"scrinium.dev/store/driver"
	"scrinium.dev/store/driver/faulty"
	"scrinium.dev/store/driver/localfs"
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

// Faulty wraps inner in a faulty.Driver for fault-injection tests.
// With no options it is a pass-through that only counts calls
// (CallCount) — useful for measuring an operation's I/O window before
// arming a torn-write sweep.
func Faulty(t testing.TB, inner driver.Driver, opts ...faulty.Option) *faulty.Driver {
	t.Helper()
	return faulty.New(inner, opts...)
}

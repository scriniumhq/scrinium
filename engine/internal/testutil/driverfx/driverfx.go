package driverfx

import (
	"testing"

	"github.com/rkurbatov/scrinium/engine/driver/localfs"
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

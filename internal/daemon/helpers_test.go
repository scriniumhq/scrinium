package daemon_test

import (
	"context"
	"crypto/sha256"
	"hash"
	"os"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver/localfs"
	"github.com/rkurbatov/scrinium/index/sqlite"
)

// openLocalDriver builds a localfs driver for tests. The
// daemon package itself uses the URI dialer; tests bypass that
// because they want to verify the daemon against an already-
// initialised store, and InitStore needs the same driver
// instance the daemon will later open.
func openLocalDriver(path string) (*localfs.Driver, error) {
	return localfs.New(path)
}

// openLocalIndex opens a sqlite index at the given file path.
func openLocalIndex(ctx context.Context, path string) (*sqlite.Index, error) {
	return sqlite.NewStore(ctx, path)
}

// testHashRegistry mirrors the daemon's defaultHashRegistry —
// duplicated rather than exported because the registry is an
// internal detail and tests shouldn't depend on it.
func testHashRegistry() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// readFile is a thin wrapper used in assertions.
func readFile(p string) ([]byte, error) {
	return os.ReadFile(p)
}

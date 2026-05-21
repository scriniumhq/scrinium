package scrinium_test

import (
	"context"
	"crypto/sha256"
	"hash"
	"os"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/index/sqlite"
	"scrinium.dev/engine/plugins"
)

// openLocalDriver builds a localfs driver for tests. The
// scrinium package itself uses the URI dialer; tests bypass
// that because they want to verify scrinium against an
// already-initialised store, and InitStore needs the same
// driver instance scrinium will later open.
func openLocalDriver(path string) (*localfs.Driver, error) {
	return localfs.New(path)
}

// openLocalIndex opens a sqlite index at the given file path.
func openLocalIndex(ctx context.Context, path string) (*sqlite.Index, error) {
	return sqlite.NewStore(ctx, path)
}

// testHashRegistry mirrors scrinium's defaultHashRegistry —
// duplicated rather than exported because the registry is an
// internal detail and tests shouldn't depend on it.
func testHashRegistry() domain.HashRegistry {
	return plugins.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// readFile is a thin wrapper used in assertions.
func readFile(p string) ([]byte, error) {
	return os.ReadFile(p)
}

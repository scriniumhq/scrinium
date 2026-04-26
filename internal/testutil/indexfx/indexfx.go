// Package indexfx supplies StoreIndex fixtures for tests outside
// the index/sqlite package. Internal tests of index/sqlite (which
// need access to the package-private *Index struct for white-box
// assertions) keep their own helpers; everyone else reaches for
// indexfx and gets a core.StoreIndex back.
//
// Backend: SQLite via index/sqlite. When alternative backends land
// (Postgres in the M5+ horizon) this package grows additional
// constructors; the existing ones do not change.
package indexfx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	sqliteindex "github.com/rkurbatov/scrinium/index/sqlite"
)

// Memory returns a fresh in-memory StoreIndex. Cleanup (Close on
// the underlying *sql.DB pool) is registered with t.Cleanup so the
// pool does not leak across tests.
//
// Use this for any test that does not specifically need on-disk
// durability — startup is faster and there is no temp file to
// clean up.
func Memory(t *testing.T) core.StoreIndex {
	t.Helper()
	idx, err := sqliteindex.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("indexfx.Memory: %v", err)
	}
	registerClose(t, idx)
	return idx
}

// Disk returns a StoreIndex backed by a real SQLite file at the
// given path. The parent directory is created if missing. Used by
// tests that exercise reopen-after-crash semantics or need to
// observe the .db / .db-wal files on disk.
//
// The caller picks the path explicitly because the typical use
// case is "create a Store at location X with index at location Y,
// kill it, reopen with the same Y" — the test owns the lifecycle.
func Disk(t *testing.T, path string) core.StoreIndex {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("indexfx.Disk: mkdir: %v", err)
	}
	idx, err := sqliteindex.NewStore(context.Background(), path)
	if err != nil {
		t.Fatalf("indexfx.Disk: %v", err)
	}
	registerClose(t, idx)
	return idx
}

// registerClose hooks the index's Close into t.Cleanup. core.StoreIndex
// does not declare Close in the interface (closing is the caller's
// responsibility per the DI contract); we type-assert and skip
// silently if the implementation does not expose one.
func registerClose(t *testing.T, idx core.StoreIndex) {
	t.Helper()
	closer, ok := idx.(interface{ Close() error })
	if !ok {
		return
	}
	t.Cleanup(func() { _ = closer.Close() })
}

// Package indexfx supplies StoreIndex fixtures for external tests.
// In-package tests of index/sqlite use their own helpers — they
// need the package-private *Index type that this package can't
// expose through the core.StoreIndex interface.
package indexfx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	sqliteindex "github.com/rkurbatov/scrinium/index/sqlite"
)

// Memory returns an in-memory sqlite-backed StoreIndex.
func Memory(t testing.TB) core.StoreIndex {
	t.Helper()
	idx, err := sqliteindex.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("indexfx.Memory: %v", err)
	}
	registerClose(t, idx)
	return idx
}

// Disk returns a StoreIndex backed by a SQLite file at path.
// Parent dir is created if missing.
func Disk(t testing.TB, path string) core.StoreIndex {
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

func registerClose(t testing.TB, idx core.StoreIndex) {
	t.Helper()
	if c, ok := idx.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = c.Close() })
	}
}

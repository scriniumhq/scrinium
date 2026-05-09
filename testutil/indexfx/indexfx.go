package indexfx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"scrinium.dev/engine/core"
	sqliteindex "scrinium.dev/engine/index/sqlite"
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
	t.Cleanup(func() { _ = idx.Close() })
}

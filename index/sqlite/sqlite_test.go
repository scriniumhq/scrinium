package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// newMemoryIndex spins up an in-memory Index for fast unit tests.
// All tests that do not specifically exercise on-disk behaviour
// (Vacuum, persistence across reopens) should use this helper.
func newMemoryIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := newStoreForTests(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// newDiskIndex creates an Index backed by a real file inside
// t.TempDir(). Use for tests that need durability or Vacuum.
func newDiskIndex(t *testing.T) (*Index, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	idx, err := newStoreForTests(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx, path
}

// --- Construction and lifecycle ---

func TestNewStore_Memory(t *testing.T) {
	idx := newMemoryIndex(t)
	v, err := idx.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Errorf("schema version = %d, want %d", v, CurrentSchemaVersion)
	}
}

func TestNewStore_File(t *testing.T) {
	idx, _ := newDiskIndex(t)
	v, err := idx.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Errorf("schema version = %d, want %d", v, CurrentSchemaVersion)
	}
}

func TestNewStore_EmptyPath(t *testing.T) {
	_, err := NewStore(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty path")
	}
}

// TestNewStore_Reopen verifies that reopening an existing on-disk
// database does not re-run migrations and preserves data. This is
// the durability smoke test; the real per-method persistence tests
// live with each method.
func TestNewStore_Reopen(t *testing.T) {
	idx, path := newDiskIndex(t)
	v1, _ := idx.SchemaVersion(context.Background())
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx2Iface, err := NewStore(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	idx2 := idx2Iface.(*Index) // SchemaVersion is a sqlite-package detail
	defer idx2.Close()
	v2, _ := idx2.SchemaVersion(context.Background())
	if v1 != v2 {
		t.Errorf("version drift across reopen: %d -> %d", v1, v2)
	}
}

// TestOpen_FutureSchemaRejected fakes a higher on-disk version and
// verifies Open returns ErrIndexSchemaMismatch. We achieve this by
// manually inserting a row claiming a higher version, closing, and
// reopening.
func TestNewStore_FutureSchemaRejected(t *testing.T) {
	idx, path := newDiskIndex(t)
	if _, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO schema_version(version, applied_at) VALUES (?, ?)`,
		CurrentSchemaVersion+1, fmtRFC3339(time.Now()),
	); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := NewStore(context.Background(), path)
	if !errors.Is(err, core.ErrIndexSchemaMismatch) {
		t.Fatalf("expected ErrIndexSchemaMismatch, got %v", err)
	}
}

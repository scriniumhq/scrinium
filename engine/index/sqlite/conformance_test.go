package sqlite

import (
	"context"
	"testing"

	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/indextest"
)

// TestConformance runs the shared StoreIndex conformance suite
// against the sqlite implementation.
//
// The factory uses an in-memory SQLite database; on-disk-only
// behaviour (vacuum, persistence-across-reopen, schema migrations)
// stays in the per-package tests where it can verify storage-level
// invariants. Conformance tests cover the API contract — anything
// that any future StoreIndex implementation (postgres, in-memory)
// must also satisfy.
func TestConformance(t *testing.T) {
	indextest.Run(t, indextest.Factory{
		Name: "sqlite-memory",
		New: func(t *testing.T) store.StoreIndex {
			t.Helper()
			idx, err := NewStore(context.Background(), ":memory:")
			if err != nil {
				t.Fatalf("NewStore(:memory:): %v", err)
			}
			t.Cleanup(func() { _ = idx.Close() })
			return idx
		},
	})
}

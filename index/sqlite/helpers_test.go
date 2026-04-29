package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/domain"
)

// Glass-box helpers shared between the per-package tests of the
// sqlite implementation. The conformance suite at
// internal/testutil/indextest does not use these — it asserts
// through the public StoreIndex API only. These helpers exist
// because some sqlite-only tests (vacuum content checks, NULL-
// column COALESCE, listing-order tests that bypass IndexManifest's
// blob-side bookkeeping) need direct SQL.

// countRows returns the number of rows in `table`. Used by
// vacuum-snapshot tests and a few other checks that verify
// implementation-internal structure.
func countRows(t *testing.T, idx *Index, table string) int {
	t.Helper()
	var n int
	q := `SELECT COUNT(*) FROM ` + table
	if err := idx.db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// insertBlob inserts a blob row directly into the blobs table,
// bypassing IndexManifest's blob-side bookkeeping (manifest_blobs
// links, ref_count from incoming manifest, etc.). Used by the
// listing-side tests that want to focus on listing semantics
// without dragging the full manifest path into setup.
func insertBlob(t *testing.T, idx *Index, ref, contentHash string, size int64, addr domain.PhysicalAddress, refCount int) {
	t.Helper()
	_, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO blobs (
			blob_ref, content_hash, original_size,
			physical_workspace, physical_path,
			pack_ref, pack_offset, pack_size,
			ref_count, last_verified_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		ref, contentHash, size,
		int(addr.Workspace), addr.Path,
		addr.PackRef, addr.Offset, addr.Size,
		refCount, fmtRFC3339(time.Now()),
	)
	if err != nil {
		t.Fatalf("insertBlob: %v", err)
	}
}

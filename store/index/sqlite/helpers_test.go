package sqlite

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/store/internal/timefmt"
)

// Glass-box helpers shared between the per-package tests of the
// sqlite implementation. The conformance suite at
// internal/testutil/indextest does not use these — it asserts
// through the public StoreIndex API only. These helpers exist
// because the remaining sqlite-only tests (vacuum content checks,
// NULL-column COALESCE on packed_blobs, blob row with packed
// metadata) need direct SQL access that the public API does not
// expose.

// countRows returns the number of rows in `table`. Used by
// vacuum-snapshot tests to verify the snapshot copied data
// across; the public API does not surface raw row counts.
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
// pack-row Resolve test where the row shape (blob_ref with
// pack_ref/offset/size populated) is not produced by the public
// IndexManifest path — only direct INSERT can stage it.
func insertBlob(t *testing.T, idx *Index, ref, contentHash string, size int64, addr domain.PhysicalAddress, refCount int) {
	t.Helper()
	_, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO blobs (
			blob_ref, content_hash, original_size, crypto_identity,
			physical_workspace, physical_path,
			pack_ref, pack_offset, pack_size,
			ref_count, last_verified_at, created_at
		) VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?, NULL, ?)`,
		ref, contentHash, size,
		int(addr.Workspace), addr.Path,
		addr.PackRef, addr.Offset, addr.Size,
		refCount, timefmt.Format(time.Now()),
	)
	if err != nil {
		t.Fatalf("insertBlob: %v", err)
	}
}

// insertManifest inserts a manifest row directly via SQL,
// bypassing IndexManifest. Used by VacuumInto_CreatesSnapshot to
// stage data so the snapshot is a meaningful copy without
// dragging blob-side bookkeeping into the test setup.
func insertManifest(t *testing.T, idx *Index, m domain.Manifest) {
	t.Helper()
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	// blob_ref is NULL for Inline manifests (§9.1.2). The list
	// helpers in tests rarely set LayoutHeader, so the common
	// path is non-NULL — but we honour the invariant either way.
	var blobRefArg any
	if m.LayoutHeader.BlobStorage == domain.LayoutInline {
		blobRefArg = nil
	} else {
		blobRefArg = string(m.BlobRef)
	}
	var retentionArg any
	if !m.RetentionUntil.IsZero() {
		retentionArg = timefmt.Format(m.RetentionUntil)
	}
	_, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO manifests (
			artifact_id, type, namespace, session_id,
			blob_ref, created_at, retention_until
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(m.ArtifactID), string(m.Type),
		m.Namespace, m.SessionID, blobRefArg,
		timefmt.Format(createdAt), retentionArg,
	)
	if err != nil {
		t.Fatalf("insertManifest: %v", err)
	}
}

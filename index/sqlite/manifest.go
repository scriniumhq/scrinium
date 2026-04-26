package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
)

// IndexManifest registers an artifact in the index. Branches on
// manifest.Type:
//   - blob: upserts the blob row, increments ref_count, inserts the
//     manifest row, links manifest -> blob.
//   - toc:  same as blob plus increments ref_count for each chunkRef
//     and links manifest -> chunks (positional).
//   - pack: registers the pack itself as one blob and inserts a row
//     into packed_blobs for each entry; manifests of packed
//     artifacts are NOT inserted into the manifests table — packed
//     artifacts are reachable through LookupPacked, not through Walk.
//
// All work happens inside a single transaction; partial registration
// is impossible.
func (i *Index) IndexManifest(
	m domain.Manifest,
	addr core.PhysicalAddress,
	chunkRefs []string,
	packedEntries []core.PackedEntry,
) error {
	const op = "IndexManifest"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	ctx := context.Background()
	if err := i.indexManifestTx(ctx, m, addr, chunkRefs, packedEntries); err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	return nil
}

// indexManifestTx runs the actual transactional work. Split out for
// readability and so the latency timer in IndexManifest stays
// honest even on the error path.
func (i *Index) indexManifestTx(
	ctx context.Context,
	m domain.Manifest,
	addr core.PhysicalAddress,
	chunkRefs []string,
	packedEntries []core.PackedEntry,
) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	switch m.Type {
	case domain.ManifestTypeBlob:
		if err := indexBlobManifest(ctx, tx, m, addr); err != nil {
			return err
		}
	case domain.ManifestTypeTOC:
		if err := indexTOCManifest(ctx, tx, m, addr, chunkRefs); err != nil {
			return err
		}
	case domain.ManifestTypePack:
		if err := indexPackManifest(ctx, tx, m, addr, packedEntries); err != nil {
			return err
		}
	default:
		return fmt.Errorf("sqlite: unknown manifest type: %q", m.Type)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// upsertBlob inserts the blob row if missing. ref_count is bumped
// in a separate step so the insert/conflict semantics stay simple.
//
// We use ON CONFLICT(blob_ref) DO NOTHING to keep the call
// idempotent: re-running IndexManifest after a partial crash
// (where the blob row already exists but the manifest row does
// not) leaves the blob untouched and proceeds with the manifest.
func upsertBlob(
	ctx context.Context,
	tx *sql.Tx,
	blobRef string,
	contentHash domain.ContentHash,
	originalSize int64,
	addr core.PhysicalAddress,
) error {
	const stmt = `
		INSERT INTO blobs (
			blob_ref, content_hash, original_size,
			physical_workspace, physical_path,
			pack_ref, pack_offset, pack_size,
			ref_count, last_verified_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)
		ON CONFLICT(blob_ref) DO NOTHING`
	_, err := tx.ExecContext(ctx, stmt,
		blobRef,
		string(contentHash),
		originalSize,
		int(addr.Workspace),
		addr.Path,
		addr.PackRef,
		addr.Offset,
		addr.Size,
		time.Now().UnixNano(),
	)
	return err
}

// bumpRefCount increments the ref_count of an existing blob.
// Returns an error if the blob row is missing — this would mean a
// caller-side bug (linking a manifest to a blob that was not
// upserted in the same transaction).
func bumpRefCount(ctx context.Context, tx *sql.Tx, blobRef string) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE blobs SET ref_count = ref_count + 1 WHERE blob_ref = ?`,
		blobRef,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("sqlite: bumpRefCount: blob %q not present", blobRef)
	}
	return nil
}

// insertManifestRow writes a row into the manifests table.
// Idempotency: ON CONFLICT(artifact_id) DO NOTHING. The same
// artifact registered twice (after a crash, for instance) is a
// no-op rather than an error — the content is by definition
// identical because ArtifactID is its hash.
func insertManifestRow(ctx context.Context, tx *sql.Tx, m domain.Manifest) error {
	const stmt = `
		INSERT INTO manifests (
			artifact_id, type, namespace, session_id,
			blob_ref, created_at, retention_until
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(artifact_id) DO NOTHING`
	var retention int64
	if !m.RetentionUntil.IsZero() {
		retention = m.RetentionUntil.UnixNano()
	}
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := tx.ExecContext(ctx, stmt,
		string(m.ArtifactID),
		string(m.Type),
		m.Namespace,
		m.SessionID,
		string(m.BlobRef),
		createdAt.UnixNano(),
		retention,
	)
	return err
}

// linkManifestToBlob inserts an edge row into manifest_blobs.
// Position is the chunk index for TOC manifests and 0 for blob
// manifests. Idempotency is provided by the PRIMARY KEY
// (artifact_id, position) and ON CONFLICT DO NOTHING.
func linkManifestToBlob(
	ctx context.Context,
	tx *sql.Tx,
	artifactID domain.ArtifactID,
	blobRef string,
	position int,
) error {
	const stmt = `
		INSERT INTO manifest_blobs (artifact_id, blob_ref, position)
		VALUES (?, ?, ?)
		ON CONFLICT(artifact_id, position) DO NOTHING`
	_, err := tx.ExecContext(ctx, stmt,
		string(artifactID), blobRef, position,
	)
	return err
}

// --- Per-type registration paths ---

func indexBlobManifest(
	ctx context.Context,
	tx *sql.Tx,
	m domain.Manifest,
	addr core.PhysicalAddress,
) error {
	if m.BlobRef == "" {
		// Inline manifests carry their bytes in the manifest itself
		// and do not have a separate blob record. We still record
		// the manifest so Walk and GetBySession can find it.
		return insertManifestRow(ctx, tx, m)
	}
	if err := upsertBlob(ctx, tx, string(m.BlobRef), m.ContentHash, m.OriginalSize, addr); err != nil {
		return err
	}
	if err := bumpRefCount(ctx, tx, string(m.BlobRef)); err != nil {
		return err
	}
	if err := insertManifestRow(ctx, tx, m); err != nil {
		return err
	}
	return linkManifestToBlob(ctx, tx, m.ArtifactID, string(m.BlobRef), 0)
}

func indexTOCManifest(
	ctx context.Context,
	tx *sql.Tx,
	m domain.Manifest,
	addr core.PhysicalAddress,
	chunkRefs []string,
) error {
	if m.BlobRef == "" {
		return fmt.Errorf("sqlite: TOC manifest %q has empty BlobRef", m.ArtifactID)
	}
	// Step 1: register the TOC blob itself.
	if err := upsertBlob(ctx, tx, string(m.BlobRef), m.ContentHash, m.OriginalSize, addr); err != nil {
		return err
	}
	if err := bumpRefCount(ctx, tx, string(m.BlobRef)); err != nil {
		return err
	}

	// Step 2: bump ref_count for each chunk and link the manifest
	// to it positionally. The chunk blobs are expected to exist —
	// the chunker.Wrapper writes them via PutBlob before the TOC
	// manifest is finalised (otherwise the TOC would point to
	// nothing). A missing chunk row is therefore an upstream bug
	// and surfaces as the bumpRefCount "not present" error.
	for pos, chunkRef := range chunkRefs {
		if err := bumpRefCount(ctx, tx, chunkRef); err != nil {
			return fmt.Errorf("toc chunk[%d] %q: %w", pos, chunkRef, err)
		}
		// Position starts at 1 for chunks because position 0 is
		// reserved for the TOC blob itself. This keeps DeleteManifest
		// logic uniform across blob and toc types.
		if err := linkManifestToBlob(ctx, tx, m.ArtifactID, chunkRef, pos+1); err != nil {
			return err
		}
	}

	// Step 3: write the manifest row itself, link to the TOC blob
	// at position 0.
	if err := insertManifestRow(ctx, tx, m); err != nil {
		return err
	}
	return linkManifestToBlob(ctx, tx, m.ArtifactID, string(m.BlobRef), 0)
}

// indexPackManifest registers a .pack volume. The pack file itself
// becomes one blob; every packed artifact is registered in
// packed_blobs but NOT in manifests — packed artifacts are not
// reachable through Walk (they live transparently inside the pack)
// and exist for LookupPacked only.
//
// ref_count semantics for the pack blob: incremented once per
// packed artifact. When all packed artifacts are deleted, the pack
// blob's ref_count drops to 0 and it becomes a GC candidate.
func indexPackManifest(
	ctx context.Context,
	tx *sql.Tx,
	m domain.Manifest,
	addr core.PhysicalAddress,
	entries []core.PackedEntry,
) error {
	if m.BlobRef == "" {
		return fmt.Errorf("sqlite: pack manifest %q has empty BlobRef", m.ArtifactID)
	}
	if err := upsertBlob(ctx, tx, string(m.BlobRef), m.ContentHash, m.OriginalSize, addr); err != nil {
		return err
	}

	const stmt = `
		INSERT INTO packed_blobs (
			artifact_id, pack_blob_ref, blob_ref,
			manifest_offset, manifest_size,
			blob_offset, blob_size,
			content_hash, namespace, session_id, pipeline_params
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(artifact_id) DO NOTHING`
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx, stmt,
			string(e.ArtifactID),
			string(m.BlobRef),
			e.BlobRef,
			e.ManifestOffset, e.ManifestSize,
			e.BlobOffset, e.BlobSize,
			string(e.ContentHash),
			e.Namespace, e.SessionID,
			e.PipelineParams,
		); err != nil {
			return err
		}
		// Each packed artifact contributes one reference to the
		// pack blob. Not bumping ref_count would break GC.
		if err := bumpRefCount(ctx, tx, string(m.BlobRef)); err != nil {
			return err
		}
	}
	return nil
}

// DeleteManifest performs the logical deletion of an artifact.
// Single transaction:
//  1. Read the (artifact_id, blob_ref) edges from manifest_blobs.
//  2. Decrement ref_count for each referenced blob.
//  3. Delete the manifest_blobs edges.
//  4. Delete the manifests row.
//
// blobRefs argument: the caller passes the same set it intends to
// be decremented. Mismatches between manifest_blobs and blobRefs
// surface as a fatal error: the index has diverged from the
// caller's view, and continuing would corrupt ref_counts. RebuildIndex
// is the recovery tool.
//
// Idempotency: deleting an already-deleted artifact is a no-op
// (returns nil). This matches the GC retry semantics.
func (i *Index) DeleteManifest(artifactID domain.ArtifactID, blobRefs []string) error {
	const op = "DeleteManifest"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	ctx := context.Background()
	if err := i.deleteManifestTx(ctx, artifactID, blobRefs); err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	return nil
}

func (i *Index) deleteManifestTx(
	ctx context.Context,
	artifactID domain.ArtifactID,
	blobRefs []string,
) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Read the actual edges from the index. Source of truth is
	// manifest_blobs, not the caller — but if the caller's set
	// disagrees we want to know.
	rows, err := tx.QueryContext(ctx,
		`SELECT blob_ref FROM manifest_blobs WHERE artifact_id = ?`,
		string(artifactID),
	)
	if err != nil {
		return err
	}
	var actual []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return err
		}
		actual = append(actual, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(actual) == 0 {
		// Nothing in the index for this artifact. Either it was
		// never registered or it has already been deleted. Either
		// way: idempotent no-op, success.
		return tx.Commit() // commit empty tx so latency timer is fair
	}

	if !sameSet(actual, blobRefs) {
		return fmt.Errorf("sqlite: DeleteManifest: blobRefs mismatch for %q "+
			"(index has %d edges, caller passed %d)",
			artifactID, len(actual), len(blobRefs))
	}

	for _, ref := range actual {
		if _, err := tx.ExecContext(ctx,
			`UPDATE blobs SET ref_count = ref_count - 1 WHERE blob_ref = ? AND ref_count > 0`,
			ref,
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM manifest_blobs WHERE artifact_id = ?`,
		string(artifactID),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM manifests WHERE artifact_id = ?`,
		string(artifactID),
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// sameSet returns true iff a and b contain the same elements
// (multiset equality). O(n+m) via map count; n is small in
// practice — manifests reference at most a few dozen blobs.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
		if count[s] < 0 {
			return false
		}
	}
	return true
}

// RebindBlob updates a blob's physical address after a successful
// Drain (HostStorage transit -> Location). ref_count and other
// counters are untouched. Idempotent: a missing blob_ref is a
// no-op (returns nil) — the same Drain may be retried after a crash
// once the rebind has already committed.
func (i *Index) RebindBlob(ctx context.Context, blobRef string, newAddr core.PhysicalAddress) error {
	const op = "RebindBlob"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	const stmt = `
		UPDATE blobs SET
			physical_workspace = ?,
			physical_path      = ?,
			pack_ref           = ?,
			pack_offset        = ?,
			pack_size          = ?
		WHERE blob_ref = ?`
	res, err := i.db.ExecContext(ctx, stmt,
		int(newAddr.Workspace),
		newAddr.Path,
		newAddr.PackRef,
		newAddr.Offset,
		newAddr.Size,
		blobRef,
	)
	if err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	// We do not check RowsAffected — a missing row is a legitimate
	// idempotent no-op.
	_ = res

	// Defensive: surface a hidden error class that some drivers
	// fold into ExecContext.
	if errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("sqlite: RebindBlob: connection done")
	}
	return nil
}

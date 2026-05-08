package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/timefmt"
)

// Resolve returns the physical address of a blob. It is the
// hot-path call on every Get; performance matters but correctness
// matters more — a stale address means reading a different file
// or a deleted one.
//
// A missing blob_ref returns errs.ErrArtifactNotFound. The choice
// of sentinel deserves a note: ErrArtifactNotFound is the
// engine-level "this thing is not here" error; from the StoreIndex
// perspective there is no separate "blob not found" — the index
// either knows where to find a blob or it does not.
func (i *Index) Resolve(blobRef string) (domain.PhysicalAddress, error) {
	const stmt = `
		SELECT physical_workspace, physical_path,
		       pack_ref, pack_offset, pack_size
		FROM blobs WHERE blob_ref = ?`
	var addr domain.PhysicalAddress
	var ws int
	err := i.db.QueryRowContext(context.Background(), stmt, blobRef).
		Scan(&ws, &addr.Path, &addr.PackRef, &addr.Offset, &addr.Size)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.PhysicalAddress{}, errs.ErrArtifactNotFound
	case err != nil:
		return domain.PhysicalAddress{}, classifyError(err)
	}
	addr.Workspace = domain.Workspace(ws)
	return addr, nil
}

// ExistsByContent is the deduplication primitive. It looks up a
// blob by the composite key (content_hash, original_size). The
// pair, not just the hash alone, because two distinct files of
// different sizes may share a hash prefix collision in pathological
// inputs — a defensive choice the format makes globally.
//
// Returns (blobRef, true, nil) when found; ("", false, nil) when
// absent; and ("", false, err) for unexpected failures.
func (i *Index) ExistsByContent(hash domain.ContentHash, originalSize int64) (string, bool, error) {
	const stmt = `
		SELECT blob_ref FROM blobs
		WHERE content_hash = ? AND original_size = ?
		LIMIT 1`
	var ref string
	err := i.db.QueryRowContext(context.Background(), stmt, string(hash), originalSize).Scan(&ref)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, classifyError(err)
	}
	return ref, true, nil
}

// ExistsByHash is the chunk-deduplication primitive used by
// chunker.Wrapper. Unlike ExistsByContent it does not check size,
// because chunks are anonymous and the chunker has no manifest
// metadata to compare against — but it DOES distinguish a normal
// blob from a tombstoned one.
//
// At the index level we don't currently track tombstones — the
// driver does (see localfs.MarkTombstone). The StoreIndex contract
// returns BlobIsTombstone when the index has a record marked as
// such; for now there is no schema field for it, so we always
// return BlobNotFound or BlobExists. The future schema migration
// that adds a tombstone column will make this method richer
// without changing its signature.
//
// This is a deliberate gap, not a bug. The current architecture
// uses the index for liveness (ref_count > 0) and the driver for
// physical state. Until M3.2 (GC) ties them together, BlobIsTombstone
// returns are not produced.
func (i *Index) ExistsByHash(hash domain.ContentHash) (domain.BlobExistStatus, error) {
	const stmt = `SELECT 1 FROM blobs WHERE content_hash = ? LIMIT 1`
	var one int
	err := i.db.QueryRowContext(context.Background(), stmt, string(hash)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.BlobNotFound, nil
	case err != nil:
		return domain.BlobNotFound, classifyError(err)
	}
	return domain.BlobExists, nil
}

// GetRefCount returns the current reference count of a blob. A
// missing blob returns errs.ErrArtifactNotFound — same rationale
// as Resolve: the index either has the blob or it does not.
//
// Returning 0 on a missing blob (instead of an error) was tempting
// for "it's just a number, callers can treat it as no references"
// — but it would hide the difference between "blob is dead, GC can
// reap" and "blob never existed". Two very different conditions.
func (i *Index) GetRefCount(blobRef string) (int, error) {
	const stmt = `SELECT ref_count FROM blobs WHERE blob_ref = ?`
	var n int
	err := i.db.QueryRowContext(context.Background(), stmt, blobRef).Scan(&n)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, errs.ErrArtifactNotFound
	case err != nil:
		return 0, classifyError(err)
	}
	return n, nil
}

// LookupPacked returns the range-read information for an
// artifact stored inside a .pack volume. The boolean second result
// is the "found" flag — false (not an error) means the artifact
// lives outside any pack; the caller should reach for Resolve(BlobRef)
// instead.
//
// On the read path, the engine consults LookupPacked first because
// it is the only way to know whether to open a sliced range read
// or a full blob. A missing packed_blobs row is the normal case:
// most artifacts are not packed.
func (i *Index) LookupPacked(artifactID domain.ArtifactID) (domain.PackedBlobInfo, bool, error) {
	const stmt = `
		SELECT pack_blob_ref, manifest_offset, manifest_size,
		       blob_offset, blob_size, COALESCE(pipeline_params, x'')
		FROM packed_blobs WHERE artifact_id = ?`
	var info domain.PackedBlobInfo
	err := i.db.QueryRowContext(context.Background(), stmt, string(artifactID)).Scan(
		&info.PackBlobRef,
		&info.ManifestOffset, &info.ManifestSize,
		&info.BlobOffset, &info.BlobSize,
		&info.PipelineParams,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.PackedBlobInfo{}, false, nil
	case err != nil:
		return domain.PackedBlobInfo{}, false, classifyError(err)
	}
	return info, true, nil
}

// --- Internal: helpers used by future iteration code. Living here
// so the read-side has a single home; iterate.go in pack 4 will
// consume them. Kept unexported and minimal. ---

// scanManifestRow scans one manifests-table row into a partial
// domain.Manifest. Used by Walk-style methods. Returns a Manifest
// with the fields we actually persist; the rest (Pipeline,
// LayoutHeader, InlineBlob, Metadata, etc.) are absent — the
// caller reconstructs them from the manifest file on disk if
// needed. This is intentional: the index is the cheap routing
// layer, not the source of truth for manifest content.
// scanManifestRow scans one row produced by the JOIN
// `manifests m LEFT JOIN blobs b USING (blob_ref)`. The blobs side
// supplies content_hash and original_size; nullable here because
// future ExternalRef manifests have no blobs row.
func scanManifestRow(rows *sql.Rows) (domain.Manifest, error) {
	var (
		artifactID, mtype, namespace, sessionID string
		createdAt                               string
		blobRef, retentionUntil                 sql.NullString
		contentHash                             sql.NullString
		originalSize                            sql.NullInt64
	)
	if err := rows.Scan(
		&artifactID, &mtype, &namespace, &sessionID,
		&blobRef, &createdAt, &retentionUntil,
		&contentHash, &originalSize,
	); err != nil {
		return domain.Manifest{}, err
	}
	m := domain.Manifest{
		ArtifactID: domain.ArtifactID(artifactID),
		Type:       domain.ManifestType(mtype),
		Namespace:  namespace,
		SessionID:  sessionID,
	}
	if blobRef.Valid {
		// NULL when LayoutHeader.BlobStorage == "Inline" (§9.1.2);
		// stays as the zero BlobRef. The router-layer doc-comment
		// on this function lists LayoutHeader as "absent — read
		// the manifest file" — same applies here: callers that
		// need to know whether this is Inline must read the file.
		m.BlobRef = domain.BlobRef(blobRef.String)
	}
	if contentHash.Valid {
		m.ContentHash = domain.ContentHash(contentHash.String)
	}
	if originalSize.Valid {
		m.OriginalSize = originalSize.Int64
	}
	t, err := timefmt.Parse(createdAt)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("scan created_at: %w", err)
	}
	m.CreatedAt = t
	if retentionUntil.Valid {
		t, err := timefmt.Parse(retentionUntil.String)
		if err != nil {
			return domain.Manifest{}, fmt.Errorf("scan retention_until: %w", err)
		}
		m.RetentionUntil = t
	}
	return m, nil
}

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// Resolve returns the physical address of a blob. It is the
// hot-path call on every Get; performance matters but correctness
// matters more — a stale address means reading a different file
// or a deleted one.
//
// A missing blob_ref returns core.ErrArtifactNotFound. The choice
// of sentinel deserves a note: ErrArtifactNotFound is the
// engine-level "this thing is not here" error; from the StoreIndex
// perspective there is no separate "blob not found" — the index
// either knows where to find a blob or it does not.
func (i *Index) Resolve(blobRef string) (core.PhysicalAddress, error) {
	const stmt = `
		SELECT physical_workspace, physical_path,
		       pack_ref, pack_offset, pack_size
		FROM blobs WHERE blob_ref = ?`
	var addr core.PhysicalAddress
	var ws int
	err := i.db.QueryRowContext(context.Background(), stmt, blobRef).
		Scan(&ws, &addr.Path, &addr.PackRef, &addr.Offset, &addr.Size)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return core.PhysicalAddress{}, core.ErrArtifactNotFound
	case err != nil:
		return core.PhysicalAddress{}, classifyError(err)
	}
	addr.Workspace = core.Workspace(ws)
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
func (i *Index) ExistsByContent(hash core.ContentHash, originalSize int64) (string, bool, error) {
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
func (i *Index) ExistsByHash(hash core.ContentHash) (core.BlobExistStatus, error) {
	const stmt = `SELECT 1 FROM blobs WHERE content_hash = ? LIMIT 1`
	var one int
	err := i.db.QueryRowContext(context.Background(), stmt, string(hash)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return core.BlobNotFound, nil
	case err != nil:
		return core.BlobNotFound, classifyError(err)
	}
	return core.BlobExists, nil
}

// GetRefCount returns the current reference count of a blob. A
// missing blob returns core.ErrArtifactNotFound — same rationale
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
		return 0, core.ErrArtifactNotFound
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
func (i *Index) LookupPacked(artifactID core.ArtifactID) (core.PackedBlobInfo, bool, error) {
	const stmt = `
		SELECT pack_blob_ref, manifest_offset, manifest_size,
		       blob_offset, blob_size, COALESCE(pipeline_params, x'')
		FROM packed_blobs WHERE artifact_id = ?`
	var info core.PackedBlobInfo
	err := i.db.QueryRowContext(context.Background(), stmt, string(artifactID)).Scan(
		&info.PackBlobRef,
		&info.ManifestOffset, &info.ManifestSize,
		&info.BlobOffset, &info.BlobSize,
		&info.PipelineParams,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return core.PackedBlobInfo{}, false, nil
	case err != nil:
		return core.PackedBlobInfo{}, false, classifyError(err)
	}
	return info, true, nil
}

// --- Internal: helpers used by future iteration code. Living here
// so the read-side has a single home; iterate.go in pack 4 will
// consume them. Kept unexported and minimal. ---

// scanManifestRow scans one manifests-table row into a partial
// core.Manifest. Used by Walk-style methods. Returns a Manifest
// with the fields we actually persist; the rest (Pipeline,
// LayoutHeader, InlineBlob, Metadata, etc.) are absent — the
// caller reconstructs them from the manifest file on disk if
// needed. This is intentional: the index is the cheap routing
// layer, not the source of truth for manifest content.
func scanManifestRow(rows *sql.Rows) (core.Manifest, error) {
	var (
		artifactID, mtype, namespace, sessionID, blobRef string
		createdAt, retentionUntil                        int64
	)
	if err := rows.Scan(
		&artifactID, &mtype, &namespace, &sessionID,
		&blobRef, &createdAt, &retentionUntil,
	); err != nil {
		return core.Manifest{}, err
	}
	m := core.Manifest{
		ArtifactID: core.ArtifactID(artifactID),
		Type:       core.ManifestType(mtype),
		Namespace:  namespace,
		SessionID:  sessionID,
		BlobRef:    core.BlobRef(blobRef),
	}
	m.CreatedAt = nsToTime(createdAt)
	if retentionUntil != 0 {
		m.RetentionUntil = nsToTime(retentionUntil)
	}
	return m, nil
}

// nsToTime converts a UnixNano-encoded timestamp to time.Time. A
// zero value remains zero — manifests with no retention store 0
// for retention_until rather than a sentinel "long-ago" timestamp.
func nsToTime(ns int64) (t time.Time) {
	if ns == 0 {
		return t
	}
	return time.Unix(0, ns)
}

// Compile guard: assert at least one expected non-trivial path of
// our error mapping. The actual classification is exercised by
// real tests; this just keeps the unused-import linter from
// removing the dependency on errors.
var _ = errors.Is
var _ = fmt.Errorf

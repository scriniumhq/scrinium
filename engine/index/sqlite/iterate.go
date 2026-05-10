package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/internal/timefmt"
)

// ListByNamespace iterates over manifests whose namespace matches
// the filter. The callback is invoked once per manifest in
// (namespace, created_at) order; cancelling via fs.SkipAll
// or any other error from the callback stops the iteration.
//
// Filter semantics match the contract of Walk in core.DataStore:
//   - "*"          — every user namespace; system.* is excluded
//   - ""           — only the default (empty) namespace
//   - <other>      — exactly that namespace
//
// Pack manifests are NEVER included; they live in packed_blobs and
// are reachable through LookupPacked instead. The manifests table
// already excludes them by construction (indexPackManifest does
// not insert a row), so this method does not need an explicit
// type filter — but the SQL keeps one for defence in depth.
func (i *Index) ListByNamespace(
	ctx context.Context,
	ns string,
	cb func(domain.Manifest) error,
) error {
	const (
		// We LEFT JOIN blobs to recover original_size and
		// content_hash, which live on the blobs row (the dedup
		// key) rather than on the manifest row. LEFT (not INNER)
		// because future ExternalRef manifests will not have a
		// matching blobs row — for them the JOIN yields NULLs and
		// scanManifestRow leaves OriginalSize at zero.
		queryDefault = `
			SELECT m.artifact_id, m.type, m.namespace, m.session_id,
			       m.blob_ref, m.created_at, m.retention_until,
			       b.content_hash, b.original_size
			FROM manifests m
			LEFT JOIN blobs b ON b.blob_ref = m.blob_ref
			WHERE m.namespace = '' AND m.type != ?
			ORDER BY m.created_at`
		queryAny = `
			SELECT m.artifact_id, m.type, m.namespace, m.session_id,
			       m.blob_ref, m.created_at, m.retention_until,
			       b.content_hash, b.original_size
			FROM manifests m
			LEFT JOIN blobs b ON b.blob_ref = m.blob_ref
			WHERE m.namespace NOT LIKE 'system.%' AND m.type != ?
			ORDER BY m.namespace, m.created_at`
		queryExact = `
			SELECT m.artifact_id, m.type, m.namespace, m.session_id,
			       m.blob_ref, m.created_at, m.retention_until,
			       b.content_hash, b.original_size
			FROM manifests m
			LEFT JOIN blobs b ON b.blob_ref = m.blob_ref
			WHERE m.namespace = ? AND m.type != ?
			ORDER BY m.created_at`
	)

	var rows *sql.Rows
	var err error
	switch ns {
	case domain.NamespaceWildcard:
		rows, err = i.db.QueryContext(ctx, queryAny, string(domain.ManifestTypePack))
	case "":
		rows, err = i.db.QueryContext(ctx, queryDefault, string(domain.ManifestTypePack))
	default:
		rows, err = i.db.QueryContext(ctx, queryExact, ns, string(domain.ManifestTypePack))
	}
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()

	return iterateManifestRows(ctx, rows, cb)
}

// GetBySession returns every ArtifactID with the given SessionID.
// Used by RollbackSession; the result set is small in practice
// (one user session, dozens to hundreds of artifacts), so we
// materialise it into a slice rather than streaming via callback.
//
// An empty SessionID guarded at the engine level (errs.ErrEmptySessionID).
// The index itself does NOT enforce that — it would be a useful
// last-line check, but consistency demands the index honour any
// query the caller passes. The engine's RollbackSession is the
// place where mass-delete safety lives.
func (i *Index) GetBySession(ctx context.Context, sessionID string) ([]domain.ArtifactID, error) {
	const stmt = `SELECT artifact_id FROM manifests WHERE session_id = ?`
	rows, err := i.db.QueryContext(ctx, stmt, sessionID)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	var out []domain.ArtifactID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, domain.ArtifactID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, classifyError(err)
	}
	return out, nil
}

// ListOrphanBlobs iterates over blobs with ref_count = 0. Used by
// the GC Agent's Mark phase — every entry is a deletion candidate.
//
// We rely on the partial index `blobs_orphan` (defined in
// schemaV1: ON blobs(ref_count) WHERE ref_count = 0) so the scan
// is cheap even on very large blob tables. SQLite uses the
// partial index automatically when the query predicate matches.
func (i *Index) ListOrphanBlobs(
	ctx context.Context,
	cb func(blobRef string) error,
) error {
	const stmt = `SELECT blob_ref FROM blobs WHERE ref_count = 0 ORDER BY blob_ref`
	rows, err := i.db.QueryContext(ctx, stmt)
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()
	return iterateBlobRefRows(ctx, rows, cb)
}

// ListUnverified iterates over blobs whose last_verified_at is
// strictly older than `before`, plus blobs that have never been
// scrubbed (last_verified_at IS NULL). Used by the Scrub Agent;
// the `before` cutoff is computed by the agent as
// now() - StoreConfig.MaxAge, possibly shifted upward for blobs
// on a CapNativeChecksum medium.
//
// NULL last_verified_at means "never verified" — those rows take
// priority and always come first under the ORDER BY (SQLite sorts
// NULLs first ASC by default).
//
// Order is by last_verified_at ascending: oldest first, which is
// what the scrub schedule wants. RFC 3339 second-precision strings
// (UTC) sort lexicographically the same as chronologically.
func (i *Index) ListUnverified(ctx context.Context, before time.Time, cb func(blobRef string) error) error {
	cutoff := timefmt.Format(before)
	const stmt = `
		SELECT blob_ref FROM blobs
		WHERE last_verified_at IS NULL OR last_verified_at < ?
		ORDER BY last_verified_at`
	rows, err := i.db.QueryContext(ctx, stmt, cutoff)
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()
	return iterateBlobRefRows(ctx, rows, cb)
}

// iterateManifestRows is the shared cursor loop for callbacks that
// take domain.Manifest. Centralised because three iteration sites
// (ListByNamespace and the two future query variants) want the
// same context check / fs.SkipAll / scan pattern.
func iterateManifestRows(
	ctx context.Context,
	rows *sql.Rows,
	cb func(domain.Manifest) error,
) error {
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		m, err := scanManifestRow(rows)
		if err != nil {
			return fmt.Errorf("sqlite: scan manifest: %w", err)
		}
		if cbErr := cb(m); cbErr != nil {
			if errors.Is(cbErr, fs.SkipAll) {
				return nil
			}
			return cbErr
		}
	}
	return classifyError(rows.Err())
}

// iterateBlobRefRows is the shared cursor loop for callbacks that
// take a single blob_ref string. ListOrphanBlobs and ListUnverified
// share this; M3.4 (RebuildIndexAgent) and any future "list blobs
// by predicate" method drop in here too — the differentiation is
// in the SELECT, not in the iteration.
func iterateBlobRefRows(
	ctx context.Context,
	rows *sql.Rows,
	cb func(blobRef string) error,
) error {
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		if cbErr := cb(ref); cbErr != nil {
			if errors.Is(cbErr, fs.SkipAll) {
				return nil
			}
			return cbErr
		}
	}
	return classifyError(rows.Err())
}

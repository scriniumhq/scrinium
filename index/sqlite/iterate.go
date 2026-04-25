package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// ListByNamespace iterates over manifests whose namespace matches
// the filter. The callback is invoked once per manifest in
// (namespace, created_at) order; cancelling via core.ErrStopWalk
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
	cb func(core.Manifest) error,
) error {
	const (
		queryDefault = `
			SELECT artifact_id, type, namespace, session_id,
			       blob_ref, created_at, retention_until
			FROM manifests
			WHERE namespace = '' AND type != ?
			ORDER BY created_at`
		queryAny = `
			SELECT artifact_id, type, namespace, session_id,
			       blob_ref, created_at, retention_until
			FROM manifests
			WHERE namespace NOT LIKE 'system.%' AND type != ?
			ORDER BY namespace, created_at`
		queryExact = `
			SELECT artifact_id, type, namespace, session_id,
			       blob_ref, created_at, retention_until
			FROM manifests
			WHERE namespace = ? AND type != ?
			ORDER BY created_at`
	)

	var rows *sql.Rows
	var err error
	switch ns {
	case "*":
		rows, err = i.db.QueryContext(ctx, queryAny, string(core.ManifestTypePack))
	case "":
		rows, err = i.db.QueryContext(ctx, queryDefault, string(core.ManifestTypePack))
	default:
		rows, err = i.db.QueryContext(ctx, queryExact, ns, string(core.ManifestTypePack))
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
// An empty SessionID guarded at the engine level (core.ErrEmptySessionID).
// The index itself does NOT enforce that — it would be a useful
// last-line check, but consistency demands the index honour any
// query the caller passes. The engine's RollbackSession is the
// place where mass-delete safety lives.
func (i *Index) GetBySession(sessionID string) ([]core.ArtifactID, error) {
	const stmt = `SELECT artifact_id FROM manifests WHERE session_id = ?`
	rows, err := i.db.QueryContext(context.Background(), stmt, sessionID)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	var out []core.ArtifactID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, core.ArtifactID(id))
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

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		if cbErr := cb(ref); cbErr != nil {
			if errors.Is(cbErr, core.ErrStopWalk) {
				return nil
			}
			return cbErr
		}
	}
	return classifyError(rows.Err())
}

// ListUnverified iterates over blobs whose last_verified_at is
// strictly older than `before`. Used by the Scrub Agent; the
// `before` cutoff is computed by the agent as
// now() - StoreConfig.MaxAge, possibly shifted upward for blobs
// on a CapNativeChecksum medium.
//
// last_verified_at == 0 means "never verified"; those rows are
// always included as long as `before` is non-zero (which it is in
// practice — agents always pass a now-minus-something cutoff).
//
// Order is by last_verified_at ascending: the oldest verifications
// come first, which is what the scrub schedule wants.
func (i *Index) ListUnverified(
	ctx context.Context,
	before time.Time,
	cb func(blobRef string) error,
) error {
	cutoff := before.UnixNano()
	const stmt = `
		SELECT blob_ref FROM blobs
		WHERE last_verified_at < ?
		ORDER BY last_verified_at`
	rows, err := i.db.QueryContext(ctx, stmt, cutoff)
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		if cbErr := cb(ref); cbErr != nil {
			if errors.Is(cbErr, core.ErrStopWalk) {
				return nil
			}
			return cbErr
		}
	}
	return classifyError(rows.Err())
}

// iterateManifestRows is the shared cursor loop for callbacks that
// take core.Manifest. Centralised because three iteration sites
// (ListByNamespace and the two future query variants) want the
// same context check / ErrStopWalk / scan pattern.
func iterateManifestRows(
	ctx context.Context,
	rows *sql.Rows,
	cb func(core.Manifest) error,
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
			if errors.Is(cbErr, core.ErrStopWalk) {
				return nil
			}
			return cbErr
		}
	}
	return classifyError(rows.Err())
}

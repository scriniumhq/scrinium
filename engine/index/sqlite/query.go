package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strconv"

	"scrinium.dev/domain"
)

// QueryByExtField streams ArtifactIDs whose projected ext field extName.field
// equals value (§9.6, read-side of the Indexer projection). proj_ext holds the
// manifest digest, so the query joins manifests for the floating ArtifactID and
// drops handle-less rows (artifact_id IS NULL → system artifacts, pack
// containers): only user-visible artifacts surface, invisibility by
// construction. v1 is equality; a richer query language is M7. The callback may
// return fs.SkipAll to stop early without an error.
func (i *Index) QueryByExtField(ctx context.Context, extName, field, value string, cb func(domain.ArtifactID) error) error {
	const stmt = `
		SELECT DISTINCT m.artifact_id
		FROM proj_ext p
		JOIN manifests m ON m.manifest_digest = p.digest
		WHERE p.ext_name = ? AND p.field = ? AND p.value = ?
		  AND m.artifact_id IS NOT NULL
		ORDER BY m.artifact_id`
	rows, err := i.db.QueryContext(ctx, stmt, extName, field, value)
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()
	return iterateArtifactIDRows(ctx, rows, cb)
}

// ListByExtField iterates over manifests whose projected ext field
// extName.field equals value, read from proj_ext (read-side of the Indexer
// projection, §9.6). It is the manifest-yielding sibling of QueryByExtField:
// where that streams bare ArtifactIDs (membership / search), this hydrates
// the index-resident Manifest — no manifest-file I/O, columns only, exactly
// as IterateManifests does — so it is the proj_ext-backed form of a listing.
// Handle-less rows (system artifacts, pack containers) are excluded by
// artifact_id IS NOT NULL. v1 is equality (a richer language is M7). The
// callback may return fs.SkipAll to stop early without an error.
func (i *Index) ListByExtField(ctx context.Context, extName, field, value string, cb func(domain.Manifest) error) error {
	const stmt = `
		SELECT ` + manifestProjection + `
		FROM manifests m
		JOIN proj_ext p ON p.digest = m.manifest_digest
		LEFT JOIN blobs b ON b.blob_ref = m.blob_ref
		WHERE p.ext_name = ? AND p.field = ? AND p.value = ?
		  AND m.artifact_id IS NOT NULL
		ORDER BY m.created_at`
	rows, err := i.db.QueryContext(ctx, stmt, extName, field, value)
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()
	return iterateManifestRows(ctx, rows, cb)
}

// QueryByUsrField is the same over proj_usr (§9.6). It returns an empty result
// (no error) when the global usr_indexing switch is off — proj_usr is then not
// maintained, so a query has nothing to match (§9.12). A field is projected
// under a single ValueKind, so exactly one value column is non-NULL per row;
// the predicate probes all three columns with the string value (plus its
// integer form when it parses), so text / number / hash fields are all
// reachable from one signature.
func (i *Index) QueryByUsrField(ctx context.Context, field, value string, cb func(domain.ArtifactID) error) error {
	on, err := i.usrIndexingOn(ctx)
	if err != nil {
		return err
	}
	if !on {
		return nil
	}
	var num sql.NullInt64
	if n, perr := strconv.ParseInt(value, 10, 64); perr == nil {
		num = sql.NullInt64{Int64: n, Valid: true}
	}
	const stmt = `
		SELECT DISTINCT m.artifact_id
		FROM proj_usr p
		JOIN manifests m ON m.manifest_digest = p.digest
		WHERE p.field = ?
		  AND (p.value_text = ? OR p.value_hash = ? OR p.value_number = ?)
		  AND m.artifact_id IS NOT NULL
		ORDER BY m.artifact_id`
	rows, err := i.db.QueryContext(ctx, stmt, field, value, value, num)
	if err != nil {
		return classifyError(err)
	}
	defer rows.Close()
	return iterateArtifactIDRows(ctx, rows, cb)
}

// usrIndexingOn reads the global store_meta.usr_indexing switch (default off)
// on the read path. Any value other than "on"/"true"/"1" — including absence —
// is off. Mirrors the tx-side gate in projection.go (write path).
func (i *Index) usrIndexingOn(ctx context.Context) (bool, error) {
	var v string
	err := i.db.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = 'usr_indexing'`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, classifyError(err)
	}
	return v == "on" || v == "true" || v == "1", nil
}

// iterateArtifactIDRows streams a single-column artifact_id result set through
// cb, honouring ctx cancellation and fs.SkipAll (mirrors iterateBlobRefRows).
func iterateArtifactIDRows(ctx context.Context, rows *sql.Rows, cb func(domain.ArtifactID) error) error {
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if cbErr := cb(domain.ArtifactID(id)); cbErr != nil {
			if errors.Is(cbErr, fs.SkipAll) {
				return nil
			}
			return cbErr
		}
	}
	return classifyError(rows.Err())
}

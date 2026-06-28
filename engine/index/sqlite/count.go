package sqlite

import "context"

// CountManifests implements index.ManifestCounter: the number of user
// manifests (artifact_id present), counted in the database rather than by
// deserialising each row. The WHERE clause mirrors IterateManifests exactly
// so the count equals what a full iteration would yield — no blobs join is
// needed, the count never touches blob columns.
func (i *Index) CountManifests(ctx context.Context) (int64, error) {
	const query = `SELECT COUNT(*) FROM manifests m WHERE m.artifact_id IS NOT NULL`
	var n int64
	if err := i.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, classifyError(err)
	}
	return n, nil
}

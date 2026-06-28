package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// csnRowID is the pinned primary key of the single index_seq row. The
// table is a one-row counter; the CHECK (id = 0) in the schema makes a
// second row impossible.
const csnRowID = 0

// nextCSN advances the change-sequence counter inside tx and returns the
// newly issued value. Every IndexManifest/DeleteManifest transaction
// calls it exactly once, so each commit gets one monotonic csn and the
// stamp shares the write transaction (ADR-106). A rolled-back transaction
// takes the bump with it — the counter never reflects an uncommitted write.
func nextCSN(ctx context.Context, tx *sql.Tx) (uint64, error) {
	if _, err := tx.ExecContext(ctx,
		`UPDATE index_seq SET csn = csn + 1 WHERE id = ?`, csnRowID,
	); err != nil {
		return 0, fmt.Errorf("sqlite: nextCSN: bump: %w", err)
	}
	var csn uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT csn FROM index_seq WHERE id = ?`, csnRowID,
	).Scan(&csn); err != nil {
		return 0, fmt.Errorf("sqlite: nextCSN: read: %w", err)
	}
	return csn, nil
}

// markPrune records csn as the prune watermark — the change-sequence at
// which a hard manifest deletion removed a row. Since(cursor) reports
// Gapped when cursor < prune_csn: the deleted row can no longer be
// enumerated, so a consumer behind the watermark re-derives by a full
// Walk (ADR-106). Callers pass the value nextCSN issued in the same delete
// transaction, so the watermark only ever grows.
func markPrune(ctx context.Context, tx *sql.Tx, csn uint64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE index_seq SET prune_csn = ? WHERE id = ?`, csn, csnRowID,
	); err != nil {
		return fmt.Errorf("sqlite: markPrune: %w", err)
	}
	return nil
}

// readToken returns the current Token — the last issued csn (ADR-106).
// The idle "did anything change?" probe must stay cheap (a single-row
// read), because polling is the floor delivery mode for a serverless
// SQLite backend. q is *sql.DB on the read path; *sql.Tx satisfies it too
// for an in-transaction read.
func readToken(ctx context.Context, q rowQueryer) (uint64, error) {
	var csn uint64
	if err := q.QueryRowContext(ctx,
		`SELECT csn FROM index_seq WHERE id = ?`, csnRowID,
	).Scan(&csn); err != nil {
		return 0, fmt.Errorf("sqlite: readToken: %w", err)
	}
	return csn, nil
}

// rowQueryer is the single-row read surface both *sql.DB and *sql.Tx
// satisfy, so the csn readers serve the read path and an in-transaction
// read without duplication.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

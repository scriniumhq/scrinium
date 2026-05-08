package sqlite

import (
	"context"
	"database/sql"
)

// runInTx runs fn inside a freshly-started transaction on db.
// Commits if fn returns nil, rolls back otherwise. The rollback
// is wired through a deferred commit-flag pattern, so panics in
// fn also roll back cleanly.
//
// Free function rather than a method because applyMigrations
// runs before *Index is constructed — the schema must be at the
// expected version before NewStore wraps the *sql.DB. Once an
// Index exists, prefer the (*Index).inTx wrapper instead.
func runInTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// inTx is the *Index counterpart to runInTx: same contract,
// scoped to the Index's own *sql.DB. Every sqlite Index method
// that opens its own transaction goes through this helper.
// Calling it makes it impossible to forget the rollback path
// or the Commit step.
//
// This is the read/write counterpart to (*Index).observe, which
// scopes metric emission. Mutating Index methods typically nest:
//
//	return i.observe("OpName", func() error {
//	    return i.inTx(ctx, func(tx *sql.Tx) error {
//	        // domain work
//	        return nil
//	    })
//	})
//
// inTx itself does NOT call observe — observability is a
// separate concern composed at the call site.
func (i *Index) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return runInTx(ctx, i.db, fn)
}

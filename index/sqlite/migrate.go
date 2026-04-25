package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// readSchemaVersion returns the highest version recorded in
// schema_version, or 0 if the table does not exist (a fresh
// database). Errors other than "table missing" are returned as-is.
func readSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	// Probe for the table first so we can distinguish a fresh DB
	// from a real query error. SQLite-portable check via
	// sqlite_master.
	const probe = `SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_version' LIMIT 1`
	var present int
	err := db.QueryRowContext(ctx, probe).Scan(&present)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("probe schema_version: %w", err)
	}

	const query = `SELECT COALESCE(MAX(version), 0) FROM schema_version`
	var version int
	if err := db.QueryRowContext(ctx, query).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// applyMigrations runs every migration whose Version is greater
// than the on-disk version, in ascending order. Each migration runs
// in its own transaction. A failure rolls the migration back and
// returns the underlying error wrapped into core.ErrIndexCorrupted —
// a partially migrated database is treated as corrupted from the
// caller's point of view (recovery is RebuildIndexAgent territory).
//
// Forward-only: an on-disk version greater than CurrentSchemaVersion
// returns core.ErrIndexSchemaMismatch (the caller built the binary
// against an older schema than the database).
func applyMigrations(ctx context.Context, db *sql.DB) error {
	current, err := readSchemaVersion(ctx, db)
	if err != nil {
		return err
	}

	if current > CurrentSchemaVersion {
		return fmt.Errorf("%w: db at v%d, binary at v%d",
			core.ErrIndexSchemaMismatch, current, CurrentSchemaVersion)
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("%w: migration v%d (%s): %v",
				core.ErrIndexCorrupted, m.Version, m.Description, err)
		}
	}
	return nil
}

// applyMigration runs all statements of one migration inside a
// single transaction. The schema_version row is inserted at the end
// of the same transaction so a crash mid-migration leaves the
// previous version intact.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback on any error path; the explicit Commit below
	// suppresses it on success.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for i, stmt := range m.Statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version(version, applied_at) VALUES (?, ?)`,
		m.Version, time.Now().UnixNano(),
	); err != nil {
		return fmt.Errorf("record version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

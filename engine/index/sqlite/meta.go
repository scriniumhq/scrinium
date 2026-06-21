package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"scrinium.dev/errs"
)

// GetMeta reads a value from store_meta. A missing key returns
// errs.ErrMetaKeyNotFound.
//
// Engine consumers (descriptor cache, last_orphan_scan_at, schema
// notes) treat store_meta as a typed singleton namespace; this
// method intentionally returns the raw string and lets the caller
// parse. Keeping serialisation out of the index keeps the store_meta
// contract trivial — encode/decode lives where the typed field
// lives.
func (i *Index) GetMeta(ctx context.Context, key string) (string, error) {
	const stmt = `SELECT value FROM store_meta WHERE key = ?`
	var val string
	err := i.db.QueryRowContext(ctx, stmt, key).Scan(&val)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", errs.ErrMetaKeyNotFound
	case err != nil:
		return "", classifyError(err)
	}
	return val, nil
}

// SetMeta writes (or overwrites) a value in store_meta. The whole
// upsert is one statement; concurrent writers go through SQLite's
// busy_timeout machinery without us doing anything special.
func (i *Index) SetMeta(ctx context.Context, key string, value string) error {
	return i.observe("SetMeta", func() error {
		const stmt = `
			INSERT INTO store_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`
		_, err := i.db.ExecContext(ctx, stmt, key, value)
		return err
	})
}

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// MarkVerified updates the last_verified_at timestamp of a blob.
// Used by the Scrub Agent after a successful integrity check.
//
// A missing blob is a no-op rather than an error: by the time the
// Scrub Agent reaches a blob, the GC may have already removed it
// in a parallel cycle. Failing here would create useless noise in
// scrub logs without helping anything.
func (i *Index) MarkVerified(blobRef string, timestamp time.Time) error {
	const op = "MarkVerified"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	const stmt = `UPDATE blobs SET last_verified_at = ? WHERE blob_ref = ?`
	_, err := i.db.ExecContext(context.Background(), stmt, fmtRFC3339(timestamp), blobRef)
	if err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	return nil
}

// DeletePacked removes every packed_blobs row whose pack_blob_ref
// matches. Called by the GC Agent right before tombstoning a pack
// volume whose ref_count has dropped to zero (every packed entry
// has been logically deleted, the pack is now an orphan).
//
// The pack's own row in `blobs` is NOT touched by this method:
// pack entries and pack metadata are different things, and the GC
// Agent removes them in separate, well-defined steps. This method
// owns only the `packed_blobs` cleanup.
//
// Idempotent: a missing pack_blob_ref returns nil.
func (i *Index) DeletePacked(packBlobRef string) error {
	const op = "DeletePacked"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	const stmt = `DELETE FROM packed_blobs WHERE pack_blob_ref = ?`
	_, err := i.db.ExecContext(context.Background(), stmt, packBlobRef)
	if err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	return nil
}

// VacuumInto creates a snapshot copy of the database at destPath.
// Used by the Snapshot Agent: a snapshot is a full self-contained
// SQLite file that RebuildIndexAgent can later open and replay.
//
// SQLite's `VACUUM INTO` runs in a single transaction and produces
// a defragmented copy. It does NOT interrupt regular reads/writes
// to the source database — readers proceed against the live WAL
// while the vacuum streams pages.
//
// destPath must point to a non-existent file. SQLite's VACUUM INTO
// refuses to overwrite. We deliberately do not pre-delete: silently
// overwriting a snapshot would mask an upstream bug where two
// SnapshotAgents fight over the same path.
//
// :memory: source is rejected — there is no on-disk content to
// snapshot. The Snapshot Agent should never call this on a memory
// index, but the explicit error is friendlier than a confusing
// SQLite-level failure.
func (i *Index) VacuumInto(ctx context.Context, destPath string) error {
	const op = "VacuumInto"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	if destPath == "" {
		return fmt.Errorf("sqlite: VacuumInto: empty destPath")
	}
	if destPath == ":memory:" {
		return fmt.Errorf("sqlite: VacuumInto: in-memory destination not supported")
	}

	// Ensure the parent directory exists. Snapshot Agents usually
	// pass paths under HostStorage that already exist, but the
	// guarantee is cheap to provide.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("sqlite: VacuumInto: prepare dest dir: %w", err)
	}

	// Reject existing files explicitly so the error is ours, not
	// SQLite's. database/sql does not accept parameter binding for
	// VACUUM INTO; we string-format the path with quoting.
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("sqlite: VacuumInto: destination already exists: %s", destPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sqlite: VacuumInto: stat dest: %w", err)
	}

	// SQLite identifier quoting: double-quote the path and escape
	// any embedded double-quote by doubling it. Single-quote would
	// also work for a string literal but VACUUM INTO accepts a
	// string literal.
	q := "VACUUM INTO '" + escapeSQLString(destPath) + "'"
	if _, err := i.db.ExecContext(ctx, q); err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	return nil
}

// escapeSQLString doubles single quotes for SQLite string literal
// safety. Used only by VacuumInto, where the path cannot be
// passed as a positional parameter.
func escapeSQLString(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// GetMeta reads a value from store_meta. A missing key returns
// core.ErrMetaKeyNotFound.
//
// Engine consumers (descriptor cache, last_orphan_scan_at, schema
// notes) treat store_meta as a typed singleton namespace; this
// method intentionally returns the raw string and lets the caller
// parse. Keeping serialisation out of the index keeps the store_meta
// contract trivial — encode/decode lives where the typed field
// lives.
func (i *Index) GetMeta(key string) (string, error) {
	const stmt = `SELECT value FROM store_meta WHERE key = ?`
	var val string
	err := i.db.QueryRowContext(context.Background(), stmt, key).Scan(&val)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", core.ErrMetaKeyNotFound
	case err != nil:
		return "", classifyError(err)
	}
	return val, nil
}

// SetMeta writes (or overwrites) a value in store_meta. The whole
// upsert is one statement; concurrent writers go through SQLite's
// busy_timeout machinery without us doing anything special.
func (i *Index) SetMeta(key string, value string) error {
	const op = "SetMeta"
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	const stmt = `
		INSERT INTO store_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`
	_, err := i.db.ExecContext(context.Background(), stmt, key, value)
	if err != nil {
		if isBusyError(err) {
			i.emitContention(op, time.Since(start))
		}
		return classifyError(err)
	}
	return nil
}

// --- Compile-time interface guarantee ---
//
// With this pack the Index struct implements every method of
// core.StoreIndex. The assertion lives at the bottom of the
// package so any future drift (a method renamed in core, a
// signature change) breaks the build immediately rather than at
// the first runtime call.
var _ core.StoreIndex = (*Index)(nil)

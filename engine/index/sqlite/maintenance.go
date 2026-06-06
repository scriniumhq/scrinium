package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/timefmt"
	"scrinium.dev/errs"
)

// MarkVerified records that a Scrub Agent has just finished a
// successful checksum verification of blobRef. The timestamp is
// the moment the verification completed; future scrubs use it
// to prioritise the oldest-verified blobs first.
//
// A missing blob is a no-op rather than an error: by the time the
// Scrub Agent reaches a blob, the GC may have already removed it
// in a parallel cycle. Failing here would create useless noise in
// scrub logs without helping anything.
func (i *Index) MarkVerified(ctx context.Context, blobRef string, timestamp time.Time) error {
	return i.observe("MarkVerified", func() error {
		const stmt = `UPDATE blobs SET last_verified_at = ? WHERE blob_ref = ?`
		_, err := i.db.ExecContext(ctx, stmt, timefmt.Format(timestamp), blobRef)
		return err
	})
}

// MarkManifestVerified records that the Scrub Agent has fully verified
// the artifact: its manifest re-hashed and (for blob-backed artifacts)
// its blobs confirmed fresh. The manifest-level stamp (schema v5)
// complements MarkVerified, which stamps physical blobs — it is the
// only place an Inline artifact's verification can be recorded, since
// Inline carries no blobs row.
//
// A missing artifact is a no-op rather than an error, mirroring
// MarkVerified: a parallel Delete may have removed the manifest between
// the scrub list and the stamp, and failing here would only add noise.
func (i *Index) MarkManifestVerified(ctx context.Context, artifactID domain.ArtifactID, timestamp time.Time) error {
	return i.observe("MarkManifestVerified", func() error {
		const stmt = `UPDATE manifests SET last_verified_at = ? WHERE artifact_id = ?`
		_, err := i.db.ExecContext(ctx, stmt, timefmt.Format(timestamp), string(artifactID))
		return err
	})
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
func (i *Index) DeletePacked(ctx context.Context, packBlobRef string) error {
	return i.observe("DeletePacked", func() error {
		const stmt = `DELETE FROM packed_blobs WHERE pack_blob_ref = ?`
		_, err := i.db.ExecContext(ctx, stmt, packBlobRef)
		return err
	})
}

// DeleteOrphanBlob removes the blobs row for blobRef only while it is
// still an orphan (ref_count = 0). The guard lives in the WHERE clause
// so the check and the delete are one atomic statement: a concurrent
// Revive that bumps ref_count between the GC Sweep and this call leaves
// the row in place. removed reports whether a row was actually deleted.
func (i *Index) DeleteOrphanBlob(ctx context.Context, blobRef string) (bool, error) {
	var removed bool
	err := i.observe("DeleteOrphanBlob", func() error {
		const stmt = `DELETE FROM blobs WHERE blob_ref = ? AND ref_count = 0`
		res, err := i.db.ExecContext(ctx, stmt, blobRef)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		removed = n > 0
		return nil
	})
	return removed, err
}

// WriteCheckpoint creates a checkpoint copy of the database at destPath.
// Used by the Checkpoint Agent: a checkpoint is a full self-contained
// SQLite file that RebuildIndexAgent can later open and replay.
//
// SQLite's `VACUUM INTO` runs in a single transaction and produces
// a defragmented copy. It does NOT interrupt regular reads/writes
// to the source database — readers proceed against the live WAL
// while the vacuum streams pages.
//
// destPath must point to a non-existent file. SQLite's VACUUM INTO
// refuses to overwrite. We deliberately do not pre-delete: silently
// overwriting a checkpoint would mask an upstream bug where two
// CheckpointAgents fight over the same path.
//
// :memory: source is rejected — there is no on-disk content to
// checkpoint. The Checkpoint Agent should never call this on a memory
// index, but the explicit error is friendlier than a confusing
// SQLite-level failure.
func (i *Index) WriteCheckpoint(ctx context.Context, destPath string) error {
	if destPath == "" {
		return fmt.Errorf("sqlite: WriteCheckpoint: empty destPath")
	}
	if destPath == ":memory:" {
		return fmt.Errorf("sqlite: WriteCheckpoint: in-memory destination not supported")
	}

	// Ensure the parent directory exists. Checkpoint Agents usually
	// pass paths that already exist, but the
	// guarantee is cheap to provide.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("sqlite: WriteCheckpoint: prepare dest dir: %w", err)
	}

	// Reject existing files explicitly so the error is ours, not
	// SQLite's. database/sql does not accept parameter binding for
	// VACUUM INTO; we string-format the path with quoting.
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("sqlite: WriteCheckpoint: destination already exists: %s", destPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sqlite: WriteCheckpoint: stat dest: %w", err)
	}

	return i.observe("WriteCheckpoint", func() error {
		// SQLite string literal: single-quote the path and escape any
		// embedded single-quote by doubling it. VACUUM INTO accepts a
		// string literal, not an identifier, so single-quote quoting
		// (escapeSQLString) is what we need here.
		q := "VACUUM INTO '" + escapeSQLString(destPath) + "'"
		_, err := i.db.ExecContext(ctx, q)
		return err
	})
}

// escapeSQLString doubles single quotes for SQLite string literal
// safety. Used only by WriteCheckpoint, where the path cannot be
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

// checkpointContentTables are the data tables copied by RestoreCheckpoint, in
// no particular order (the schema declares no foreign keys, so order is
// irrelevant). It deliberately excludes:
//   - schema_version: owned by the target's own migration state, set when the
//     target index was opened; copying the checkpoint's rows would corrupt it.
//   - store_meta: the descriptor L2 cache and scan timestamps are store-session
//     projection the Store re-establishes on open, not index content.
//
// This list must track the data tables in schemaBaseline (engine/index/
// sqlite/schema.go); a new content table added there must be added here too.
var checkpointContentTables = []string{
	"blobs",
	"manifests",
	"manifest_blobs",
	"packed_blobs",
	"ext_meta",
	"ext_data",
}

// RestoreCheckpoint loads a checkpoint file (a self-contained SQLite index
// produced by WriteCheckpoint) into this index. It is the read side of the
// CheckpointWriter/CheckpointRestorer pair and the starting point of the
// rebuild fast-path: a freshly created, empty index is filled from a recent
// checkpoint, after which the caller replays the manifest tail.
//
// The source is first opened so its schema migrates forward to the running
// code's version — and a checkpoint written by NEWER code is refused, because
// NewStore rejects a schema newer than it understands. The source is then
// closed (flushing its WAL) and ATTACHed; its content tables are copied in one
// transaction on a single pinned connection (ATTACH is connection-local, so
// the copy must not be scattered across the pool). store_meta and
// schema_version are intentionally not copied (see checkpointContentTables).
//
// :memory: and a missing source are rejected with an explicit error.
func (i *Index) RestoreCheckpoint(ctx context.Context, srcPath string) error {
	if srcPath == "" {
		return fmt.Errorf("sqlite: RestoreCheckpoint: empty srcPath")
	}
	if srcPath == ":memory:" {
		return fmt.Errorf("sqlite: RestoreCheckpoint: in-memory source not supported")
	}
	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("sqlite: RestoreCheckpoint: stat source: %w", err)
	}

	// Migrate the checkpoint to the running schema (and refuse a newer one) by
	// opening it; close before ATTACH so its WAL is checkpointed into the file.
	src, err := NewStore(ctx, srcPath)
	if err != nil {
		return fmt.Errorf("sqlite: RestoreCheckpoint: open/migrate source: %w", err)
	}
	if err := src.Close(); err != nil {
		return fmt.Errorf("sqlite: RestoreCheckpoint: close source: %w", err)
	}

	return i.observe("RestoreCheckpoint", func() error {
		// ATTACH is connection-local; pin one connection for the whole
		// attach/copy/detach sequence, then return it to the pool clean.
		conn, err := i.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("acquire conn: %w", err)
		}
		defer conn.Close()

		// ATTACH/DETACH take a string literal, not a bound parameter.
		if _, err := conn.ExecContext(ctx, "ATTACH DATABASE '"+escapeSQLString(srcPath)+"' AS ckpt"); err != nil {
			return fmt.Errorf("attach checkpoint: %w", err)
		}
		// Detach before the connection returns to the pool, even on failure,
		// so no pooled connection carries a stale attachment or a lock on the
		// (soon-deleted) temp file. Use a cancellation-free context: cleanup
		// must run even when ctx is already done.
		defer func() {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "DETACH DATABASE ckpt")
		}()

		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin restore tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		for _, table := range checkpointContentTables {
			// INSERT OR REPLACE keeps the restore faithful and idempotent: the
			// target is normally empty, but a re-run (or a stray pre-seeded
			// row) lets the checkpoint's value win rather than erroring.
			stmt := "INSERT OR REPLACE INTO main." + table + " SELECT * FROM ckpt." + table
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("copy %s: %w", table, err)
			}
		}
		return tx.Commit()
	})
}

// CheckpointMeta reads one store_meta value from a checkpoint file without
// restoring it — used by the Store layer to verify a checkpoint's identity
// before a restore. The source is opened (migrating its schema forward, and
// refusing one newer than the code) and closed; an absent key returns
// ("", nil) so the caller can tell "no such metadata" from a read error.
func (i *Index) CheckpointMeta(ctx context.Context, srcPath, key string) (string, error) {
	if srcPath == "" || srcPath == ":memory:" {
		return "", fmt.Errorf("sqlite: CheckpointMeta: invalid srcPath %q", srcPath)
	}
	if _, err := os.Stat(srcPath); err != nil {
		return "", fmt.Errorf("sqlite: CheckpointMeta: stat source: %w", err)
	}
	src, err := NewStore(ctx, srcPath)
	if err != nil {
		return "", fmt.Errorf("sqlite: CheckpointMeta: open source: %w", err)
	}
	defer func() { _ = src.Close() }()

	v, err := src.GetMeta(ctx, key)
	if errors.Is(err, errs.ErrMetaKeyNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: CheckpointMeta: read %q: %w", key, err)
	}
	return v, nil
}

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

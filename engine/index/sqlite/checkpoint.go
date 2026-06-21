package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"scrinium.dev/errs"
)

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

// escapeSQLString doubles embedded single quotes so s is safe to embed in
// a SQLite string literal ('...'). It is used by WriteCheckpoint
// (VACUUM INTO '<path>') and RestoreCheckpoint (ATTACH DATABASE '<path>'):
// neither statement accepts a bound parameter for the path — VACUUM INTO
// and ATTACH take a string literal, not a placeholder — so the path must
// be quoted inline. Doubling the single quote is the only escaping a
// SQLite string literal requires; the surrounding quotes keep the path
// from being parsed as SQL.
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
	"manifest_handles",
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

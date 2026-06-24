package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"scrinium.dev/domain"
	"scrinium.dev/engine/customindex"
)

// snapshotIndexers returns the registered custom indexes that implement the
// Indexer capability, copied under the lock so dispatch iterates a stable list
// even if a concurrent Close nils the map (mirrors snapshotSubscribers).
func (i *Index) snapshotIndexers() []customindex.CustomIndex {
	i.ciMu.Lock()
	defer i.ciMu.Unlock()
	if len(i.ciByName) == 0 {
		return nil
	}
	out := make([]customindex.CustomIndex, 0, len(i.ciByName))
	for _, ci := range i.ciByName {
		if _, ok := ci.(customindex.Indexer); ok {
			out = append(out, ci)
		}
	}
	return out
}

// applyIndexers runs every registered Indexer over m in the index-write
// transaction (§9.2.1): each index writes its OWN tables through its Substrate
// and RETURNS Projections the core writes into proj_ext / proj_usr. The core
// stamps digest and ext_name (= Name()) — an index cannot project under
// another's name (Principle 8). Idempotent (INSERT OR REPLACE) so a crash-replay
// of IndexManifest overwrites identically (§9.10). usr projections are dropped
// unless the global usr_indexing switch is on (read once per call, cached).
func (i *Index) applyIndexers(ctx context.Context, tx *sql.Tx, m domain.Manifest) error {
	idxs := i.snapshotIndexers()
	if len(idxs) == 0 {
		return nil
	}
	digest := string(m.Digest)
	usrOn := -1 // tri-state: -1 unknown, 0 off, 1 on
	for _, ci := range idxs {
		name := ci.Name()
		sub := newSqliteSubstrate(name)
		sub.useTx(tx)
		projs, err := ci.(customindex.Indexer).Index(ctx, sub, m)
		if err != nil {
			return fmt.Errorf("indexer %q index: %w", name, err)
		}
		for _, p := range projs {
			switch p.Pocket {
			case customindex.PocketExt:
				if err := upsertProjExt(ctx, tx, digest, name, p.Field, p.Value); err != nil {
					return fmt.Errorf("indexer %q ext field %q: %w", name, p.Field, err)
				}
			case customindex.PocketUsr:
				if usrOn == -1 {
					on, err := usrIndexingEnabled(ctx, tx)
					if err != nil {
						return fmt.Errorf("read usr_indexing: %w", err)
					}
					if on {
						usrOn = 1
					} else {
						usrOn = 0
					}
				}
				if usrOn == 0 {
					continue // global usr_indexing off — usr projection disabled
				}
				if err := upsertProjUsr(ctx, tx, digest, p.Field, p.Kind, p.Value); err != nil {
					return fmt.Errorf("indexer %q usr field %q: %w", name, p.Field, err)
				}
			default:
				return fmt.Errorf("indexer %q field %q: unknown pocket %d", name, p.Field, p.Pocket)
			}
		}
	}
	return nil
}

// applyUnindexers runs every registered Indexer's Unindex over the manifest
// being deleted, in the delete transaction (§9.2.1) — the symmetric inverse of
// applyIndexers. Each index removes the rows it wrote to its OWN tables through
// its Substrate. The core has already removed the built-in proj_* rows by digest
// (deleteProjections); this covers the own-table side a digest alone cannot
// reach. The manifest passed carries the indexed identity (ArtifactID/Digest);
// its body (Ext) is not available at delete time, so an Unindex that needs the
// payload recovers it from its own tables (as fspathindex does).
func (i *Index) applyUnindexers(ctx context.Context, tx *sql.Tx, m domain.Manifest) error {
	idxs := i.snapshotIndexers()
	if len(idxs) == 0 {
		return nil
	}
	for _, ci := range idxs {
		name := ci.Name()
		sub := newSqliteSubstrate(name)
		sub.useTx(tx)
		if err := ci.(customindex.Indexer).Unindex(ctx, sub, m); err != nil {
			return fmt.Errorf("indexer %q unindex: %w", name, err)
		}
	}
	return nil
}

// deleteProjections removes every built-in projection row for digest, in the
// delete transaction. The core owns proj_*, so it removes them by digest — the
// symmetric inverse of having written them, and robust to an index toggled off
// since the write (no orphan rows). An index's OWN tables (Substrate, §9.7) are
// cleaned by Unindex (applyUnindexers, wired in deleteManifestTx). proj_* delete
// needs only the digest, so it is handled here.
func deleteProjections(ctx context.Context, tx *sql.Tx, digest string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM proj_ext WHERE manifest_digest = ?`, digest); err != nil {
		return fmt.Errorf("delete proj_ext: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM proj_usr WHERE manifest_digest = ?`, digest); err != nil {
		return fmt.Errorf("delete proj_usr: %w", err)
	}
	return nil
}

// --- proj_* row writers ---

func upsertProjExt(ctx context.Context, tx *sql.Tx, digest, extName, field, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO proj_ext (manifest_digest, ext_name, field, value) VALUES (?, ?, ?, ?)`,
		digest, extName, field, value)
	return err
}

// upsertProjUsr writes one proj_usr row, placing Value in the column its Kind
// selects. KindNumber parses Value as a base-10 int64 (a non-integer is a
// projection error — the index declared the field numeric). KindHash stores the
// index-supplied hex hash verbatim (the index computed it from the decoded
// value; opaque bytes never reach the index).
func upsertProjUsr(ctx context.Context, tx *sql.Tx, digest, field string, kind customindex.ValueKind, value string) error {
	var (
		text sql.NullString
		num  sql.NullInt64
		hash sql.NullString
	)
	switch kind {
	case customindex.KindText:
		text = sql.NullString{String: value, Valid: true}
	case customindex.KindNumber:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("value %q not an integer: %w", value, err)
		}
		num = sql.NullInt64{Int64: n, Valid: true}
	case customindex.KindHash:
		hash = sql.NullString{String: value, Valid: true}
	default:
		return fmt.Errorf("unknown value kind %d", kind)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO proj_usr (manifest_digest, field, value_text, value_number, value_hash) VALUES (?, ?, ?, ?, ?)`,
		digest, field, text, num, hash)
	return err
}

// usrIndexingEnabled reports the global store_meta.usr_indexing switch
// (default off). Any value other than "on"/"true"/"1" — including absence
// — is off. It takes a sqlExecutor so the read path (*sql.DB, via
// QueryByUsrField) and the write path (*sql.Tx, via applyIndexers) share
// one gate; wrapping the error is left to the caller.
func usrIndexingEnabled(ctx context.Context, ex sqlExecutor) (bool, error) {
	var v string
	err := ex.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = 'usr_indexing'`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return v == "on" || v == "true" || v == "1", nil
}

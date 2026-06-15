package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"scrinium.dev/engine/extension/customindex"
	"scrinium.dev/engine/internal/timefmt"
)

// Extensions returns the registry for installing index extensions
// against this backend. Method on the concrete *Index type rather
// than on store.StoreIndex — see ADR-49 for the rationale (avoids
// a core ↔ index import cycle and respects backends that don't
// support extensions).
//
// Implements customindex.ExtensionHost.
func (i *Index) Extensions() customindex.ExtensionRegistry {
	return &extensionRegistry{idx: i}
}

// ListExtensions enumerates currently-registered extensions,
// returning each one's name and persisted schema version. Names
// appear in unspecified order — callers wanting deterministic
// listings sort the result. Useful for diagnostics and stats
// endpoints; not part of any contract surface.
//
// Returns an empty slice (never nil) when no extensions are
// registered.
//
// Implements customindex.ExtensionLister.
func (i *Index) ListExtensions() []customindex.ExtensionInfo {
	i.extMu.Lock()
	defer i.extMu.Unlock()
	if len(i.extByName) == 0 {
		return []customindex.ExtensionInfo{}
	}
	out := make([]customindex.ExtensionInfo, 0, len(i.extByName))
	for name, ext := range i.extByName {
		out = append(out, customindex.ExtensionInfo{
			Name:          name,
			SchemaVersion: ext.SchemaVersion(),
		})
	}
	return out
}

// extensionRegistry is the concrete implementation of
// customindex.ExtensionRegistry for the sqlite backend. Holds a back-
// reference to the index and runs Register inside the index's
// extension lock plus a fresh transaction.
type extensionRegistry struct {
	idx *Index
}

func (r *extensionRegistry) Register(ctx context.Context, ext customindex.CustomIndex) error {
	if ext == nil {
		return fmt.Errorf("sqlite: Register: nil extension")
	}
	name := ext.Name()
	if name == "" {
		return fmt.Errorf("sqlite: Register: empty extension name")
	}

	r.idx.extMu.Lock()
	defer r.idx.extMu.Unlock()

	if _, exists := r.idx.extByName[name]; exists {
		return fmt.Errorf("%w: %q", customindex.ErrExtensionExists, name)
	}

	// store is captured inside the tx closure and reused after
	// commit (useDB switch + cache insertion).
	var store *sqliteExtStore

	// Begin a transaction for Setup. The persisted version is read,
	// Setup runs against a tx-bound store, and the new version is
	// written — all atomically. On error nothing changes on disk
	// nor in the in-memory dispatch maps.
	err := r.idx.inTx(ctx, func(tx *sql.Tx) error {
		oldVersion, err := loadExtensionVersion(ctx, tx, name)
		if err != nil {
			return fmt.Errorf("sqlite: Register: load version: %w", err)
		}
		newVersion := ext.SchemaVersion()
		if newVersion < oldVersion {
			return fmt.Errorf("%w: %q v%d → v%d",
				customindex.ErrSchemaRegression, name, oldVersion, newVersion)
		}

		store = newSqliteExtStore(name)
		store.useTx(tx)

		if err := ext.Setup(ctx, store, oldVersion); err != nil {
			return fmt.Errorf("extension %q setup: %w", name, err)
		}

		if err := upsertExtensionVersion(ctx, tx, name, newVersion); err != nil {
			return fmt.Errorf("sqlite: Register: persist version: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// After commit the store flips to db-mode for read-side use
	// after Setup. The extension may have captured the store
	// reference during Setup; the swap is transparent.
	store.useDB(r.idx.db)

	// Cache subscriptions and the long-lived store. Subscribe is
	// called once at registration and the result is final; if a
	// later restart re-creates the extension instance, that's a
	// new Register call and the new Subscribe() result wins.
	r.idx.extByName[name] = ext
	r.idx.extStores[name] = store
	for _, kind := range ext.Subscribe() {
		r.idx.extByKind[kind] = append(r.idx.extByKind[kind], ext)
	}
	return nil
}

// loadExtensionVersion returns the persisted schema_version for the
// given extension name, or 0 if no row exists yet (first
// registration). Errors are infrastructure-level only — a missing
// row is not an error.
func loadExtensionVersion(ctx context.Context, tx *sql.Tx, name string) (int, error) {
	const stmt = `SELECT schema_version FROM ext_meta WHERE extension = ?`
	var v int
	err := tx.QueryRowContext(ctx, stmt, name).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, err
	}
	return v, nil
}

// upsertExtensionVersion writes the new schema_version. The
// registered_at column is refreshed on every update — it's a
// diagnostic, not a correctness primitive.
func upsertExtensionVersion(ctx context.Context, tx *sql.Tx, name string, version int) error {
	const stmt = `
		INSERT INTO ext_meta (extension, schema_version, registered_at)
		VALUES (?, ?, ?)
		ON CONFLICT (extension) DO UPDATE SET
			schema_version = excluded.schema_version,
			registered_at  = excluded.registered_at`
	_, err := tx.ExecContext(ctx, stmt, name, version, timefmt.Format(time.Now()))
	return err
}

// --- ExtensionStore implementation ---

// sqliteExtStore is the customindex.ExtensionStore implementation for the
// sqlite backend. It carries an executor — either *sql.Tx (during
// Setup or Apply) or *sql.DB (for read-side after Setup) — and
// dispatches every method through it.
//
// The two states are tracked via an atomic.Pointer so a Setup
// callback that captures the store reference observes the swap to
// db-mode atomically once Register commits. Without atomicity, a
// concurrent reader caller could race against the swap and see a
// stale tx pointer pointing at a closed transaction.
type sqliteExtStore struct {
	extName  string
	executor atomic.Pointer[sqlExecutor]
}

// sqlExecutor is the minimal SQL surface both *sql.Tx and *sql.DB
// satisfy. Internal — extensions never see this type.
type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func newSqliteExtStore(name string) *sqliteExtStore {
	return &sqliteExtStore{extName: name}
}

func (s *sqliteExtStore) useTx(tx *sql.Tx) {
	var ex sqlExecutor = tx
	s.executor.Store(&ex)
}

func (s *sqliteExtStore) useDB(db *sql.DB) {
	var ex sqlExecutor = db
	s.executor.Store(&ex)
}

func (s *sqliteExtStore) exec() sqlExecutor {
	p := s.executor.Load()
	if p == nil {
		// Should never happen — newSqliteExtStore is followed
		// immediately by useTx. Defensive.
		return nil
	}
	return *p
}

func (s *sqliteExtStore) Put(table, key string, value []byte) error {
	const stmt = `
		INSERT INTO ext_data (extension, table_name, key, value)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (extension, table_name, key) DO UPDATE
			SET value = excluded.value`
	ex := s.exec()
	if ex == nil {
		return errExtStoreClosed
	}
	_, err := ex.ExecContext(context.Background(), stmt,
		s.extName, table, key, value)
	return err
}

func (s *sqliteExtStore) Get(table, key string) ([]byte, bool, error) {
	const stmt = `
		SELECT value FROM ext_data
		WHERE extension = ? AND table_name = ? AND key = ?`
	ex := s.exec()
	if ex == nil {
		return nil, false, errExtStoreClosed
	}
	var value []byte
	err := ex.QueryRowContext(context.Background(), stmt,
		s.extName, table, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return value, true, nil
}

func (s *sqliteExtStore) Delete(table, key string) error {
	const stmt = `
		DELETE FROM ext_data
		WHERE extension = ? AND table_name = ? AND key = ?`
	ex := s.exec()
	if ex == nil {
		return errExtStoreClosed
	}
	_, err := ex.ExecContext(context.Background(), stmt,
		s.extName, table, key)
	return err
}

func (s *sqliteExtStore) DeletePrefix(table, prefix string) error {
	if prefix == "" {
		return customindex.ErrEmptyPrefix
	}
	upper, hasUpper := prefixUpperBound(prefix)
	ex := s.exec()
	if ex == nil {
		return errExtStoreClosed
	}
	if hasUpper {
		const stmt = `
			DELETE FROM ext_data
			WHERE extension = ? AND table_name = ?
			  AND key >= ? AND key < ?`
		_, err := ex.ExecContext(context.Background(), stmt,
			s.extName, table, prefix, upper)
		return err
	}
	// prefix consisted of all 0xFF bytes — no upper bound.
	const stmt = `
		DELETE FROM ext_data
		WHERE extension = ? AND table_name = ? AND key >= ?`
	_, err := ex.ExecContext(context.Background(), stmt,
		s.extName, table, prefix)
	return err
}

func (s *sqliteExtStore) Scan(table, prefix string, cb func(key string, value []byte) error) error {
	ex := s.exec()
	if ex == nil {
		return errExtStoreClosed
	}

	var rows *sql.Rows
	var err error
	if prefix == "" {
		const stmt = `
			SELECT key, value FROM ext_data
			WHERE extension = ? AND table_name = ?
			ORDER BY key`
		rows, err = ex.QueryContext(context.Background(), stmt,
			s.extName, table)
	} else {
		upper, hasUpper := prefixUpperBound(prefix)
		if hasUpper {
			const stmt = `
				SELECT key, value FROM ext_data
				WHERE extension = ? AND table_name = ?
				  AND key >= ? AND key < ?
				ORDER BY key`
			rows, err = ex.QueryContext(context.Background(), stmt,
				s.extName, table, prefix, upper)
		} else {
			const stmt = `
				SELECT key, value FROM ext_data
				WHERE extension = ? AND table_name = ? AND key >= ?
				ORDER BY key`
			rows, err = ex.QueryContext(context.Background(), stmt,
				s.extName, table, prefix)
		}
	}
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if cbErr := cb(key, value); cbErr != nil {
			if errors.Is(cbErr, customindex.ErrStopScan) {
				return nil
			}
			return cbErr
		}
	}
	return rows.Err()
}

func (s *sqliteExtStore) Inc(table, key string, delta int64) (int64, error) {
	ex := s.exec()
	if ex == nil {
		return 0, errExtStoreClosed
	}

	// RMW under the surrounding transaction. Inside Apply this is
	// the active *sql.Tx; under the index write-lock there is no
	// concurrent writer to this row. Outside of Apply (read-side
	// "after Setup" path) Inc still works but loses transactional
	// guarantees with main-table writes — Inc-from-read-side is a
	// rare operation, typically counters are bumped from Apply.
	const selectStmt = `
		SELECT value FROM ext_data
		WHERE extension = ? AND table_name = ? AND key = ?`
	var current int64
	var raw []byte
	err := ex.QueryRowContext(context.Background(), selectStmt,
		s.extName, table, key).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		current = 0
	case err != nil:
		return 0, err
	default:
		if len(raw) != 8 {
			return 0, fmt.Errorf("sqlite: Inc: existing value at %q/%q has %d bytes, expected 8",
				table, key, len(raw))
		}
		current = int64(binary.BigEndian.Uint64(raw))
	}

	next := current + delta
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, uint64(next))

	const upsertStmt = `
		INSERT INTO ext_data (extension, table_name, key, value)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (extension, table_name, key) DO UPDATE
			SET value = excluded.value`
	if _, err := ex.ExecContext(context.Background(), upsertStmt,
		s.extName, table, key, encoded); err != nil {
		return 0, err
	}
	return next, nil
}

// errExtStoreClosed is the internal error returned when the store
// is used after Index.Close has zeroed its executor pointer. Not
// expected in well-behaved code; surfaces as a normal error so
// the caller can log and recover.
var errExtStoreClosed = errors.New("sqlite: ext store used after close")

// prefixUpperBound returns the smallest string strictly greater
// than every string starting with prefix. The second return is
// false when prefix consists entirely of 0xFF bytes — there is no
// finite upper bound, callers fall back to "key >= prefix" only.
//
// Example: prefixUpperBound("foo") == ("fop", true).
// Example: prefixUpperBound("\xFF\xFF") == ("", false).
func prefixUpperBound(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			b = b[:i+1]
			b[i]++
			return string(b), true
		}
	}
	return "", false
}

// --- Dispatch ---

// snapshotSubscribers returns a copy of the subscriber slice for the
// given kind, taken under the extension lock. The copy ensures the
// dispatcher iterates a stable list even if a concurrent Close
// nilled out the maps.
func (i *Index) snapshotSubscribers(kind customindex.EventKind) []customindex.CustomIndex {
	i.extMu.Lock()
	defer i.extMu.Unlock()
	subs := i.extByKind[kind]
	if len(subs) == 0 {
		return nil
	}
	out := make([]customindex.CustomIndex, len(subs))
	copy(out, subs)
	return out
}

// dispatchExtensions invokes every subscriber's Apply for the given
// kind under the active transaction. Returns the first error,
// which the caller must let propagate so the surrounding
// transaction rolls back.
//
// A new sqliteExtStore is allocated per (Apply call, extension)
// pair — short-lived, tx-scoped, never captured outside Apply.
// The long-lived store the extension captured during Setup is a
// different instance that lives on the index until Close.
func (i *Index) dispatchExtensions(
	ctx context.Context,
	tx *sql.Tx,
	kind customindex.EventKind,
	args customindex.EventArgs,
) error {
	subs := i.snapshotSubscribers(kind)
	for _, ext := range subs {
		store := newSqliteExtStore(ext.Name())
		store.useTx(tx)
		if err := ext.Apply(ctx, store, kind, args); err != nil {
			return fmt.Errorf("extension %q apply (%s): %w",
				ext.Name(), kind.String(), err)
		}
	}
	return nil
}

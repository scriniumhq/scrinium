//go:build !sqlite_cgo

package sqlite

import (
	"database/sql"

	// modernc.org/sqlite registers the "sqlite" driver name on init.
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver registered by the imported
// SQLite package. It is referenced once in Open. Centralised here
// so the cgo and pure-Go variants stay in sync.
const driverName = "sqlite"

// openSQL opens a database/sql connection for the given DSN. It
// exists as a thin wrapper so the tag-selected file is the only
// place that names the driver string; everything else in the
// package uses *sql.DB directly.
func openSQL(dsn string) (*sql.DB, error) {
	return sql.Open(driverName, dsn)
}

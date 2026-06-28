//go:build sqlite_cgo

package sqlite

import (
	"database/sql"

	// mattn/go-sqlite3 registers the "sqlite3" driver name on init.
	_ "github.com/mattn/go-sqlite3"
)

const driverName = "sqlite3"

// driverLabel is the human-facing backend name surfaced by Index.DriverName.
const driverLabel = "sqlite (mattn)"

func openSQL(dsn string) (*sql.DB, error) {
	return sql.Open(driverName, dsn)
}

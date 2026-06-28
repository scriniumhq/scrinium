//go:build !sqlite_cgo

package sqlite

import (
	"errors"

	sqlitedrv "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// typedBusy reports whether err is a SQLite contention error, detected by the
// driver's typed result code rather than by message text. This is the default
// (modernc) build; the sqlite_cgo build provides its own typedBusy. The code
// is masked to its primary byte so the extended forms (SQLITE_BUSY_SNAPSHOT,
// SQLITE_LOCKED_SHAREDCACHE, …) are caught as well.
func typedBusy(err error) bool {
	var se *sqlitedrv.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xFF {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}

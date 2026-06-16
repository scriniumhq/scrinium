package sqlite

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"scrinium.dev/engine/index"
	"scrinium.dev/internal/uri"
)

// init registers the sqlite:// scheme with the index registry.
// Importing index/sqlite is enough to activate the dialer.
func init() {
	index.RegisterDialer("sqlite", openSQLiteURI)
}

// openSQLiteURI builds a sqlite Index from a parsed URI.
// Forms accepted (path resolution shared with file:// and the
// assembler via scrinium.dev/internal/uri):
//
//   - sqlite:///abs/path/to.db        canonical absolute
//   - sqlite://~/rel/to.db            ~ → $HOME
//   - sqlite://./rel/to.db            . → current directory
//   - sqlite://:memory:               in-memory database
//
// The :memory: form is special-cased so tests don't have to
// resolve a path that doesn't exist.
//
// Query parameters: not currently honoured. SQLite tunables
// (busy_timeout, journal_mode, synchronous) use NewStore's
// internal defaults — already at recommended values
// (WAL + busy_timeout=5000ms). Exposing them through query
// params requires extending the IndexOption surface; tracked
// as a follow-up.
func openSQLiteURI(ctx context.Context, u *url.URL, opts ...index.IndexOption) (index.StoreIndex, error) {
	// Special form: sqlite://:memory: → in-memory DB. The URL
	// parser keeps ":memory:" in u.Host because the colons
	// look like an authority-with-port; we recognise it
	// explicitly before path resolution.
	if u.Host == ":memory:" || u.Path == "/:memory:" {
		return NewStore(ctx, ":memory:", opts...)
	}

	abs, err := uri.ResolveLocalPath(u)
	if err != nil {
		switch {
		case errors.Is(err, uri.ErrUnsupportedHost):
			return nil, fmt.Errorf("sqlite: sqlite:// host %q not supported (sqlite:///abs, sqlite://~/rel)", u.Host)
		case errors.Is(err, uri.ErrEmptyPath):
			return nil, fmt.Errorf("sqlite: sqlite:// URI has empty path")
		default:
			return nil, fmt.Errorf("sqlite: %w", err)
		}
	}
	return NewStore(ctx, abs, opts...)
}

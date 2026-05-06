package sqlite

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/index"
)

// init registers the sqlite:// scheme with the index registry.
// Importing index/sqlite is enough to activate the dialer.
func init() {
	index.RegisterDialer("sqlite", openSQLiteURI)
}

// openSQLiteURI builds a sqlite Index from a parsed URI.
// Forms accepted:
//
//   - sqlite:///abs/path/to.db        canonical absolute
//   - sqlite://~/relative.db          tilde via host="~"
//   - sqlite://./relative.db          cwd-relative via host="."
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
func openSQLiteURI(ctx context.Context, u *url.URL, opts ...index.IndexOption) (core.StoreIndex, error) {
	// Special form: sqlite://:memory: → in-memory DB. The URL
	// parser keeps ":memory:" in u.Host because the colons
	// look like an authority-with-port; we recognise it
	// explicitly before path resolution.
	if u.Host == ":memory:" || u.Path == "/:memory:" {
		return NewStore(ctx, ":memory:", opts...)
	}

	var path string
	switch u.Host {
	case "":
		path = u.Path
	case "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("sqlite: expand ~: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(u.Path, "/"))
	case ".":
		path = "." + u.Path
	default:
		return nil, fmt.Errorf("sqlite: sqlite:// host %q not supported (use sqlite:///path or sqlite://~/path)", u.Host)
	}

	if path == "" {
		return nil, fmt.Errorf("sqlite: sqlite:// URI has empty path")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: absolute path: %w", err)
	}
	return NewStore(ctx, abs, opts...)
}

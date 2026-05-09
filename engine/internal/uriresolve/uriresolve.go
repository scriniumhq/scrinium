// Package uriresolve resolves the local-path forms shared by the
// file:// and sqlite:// URI schemes.
//
// Both schemes accept the same shapes:
//
//   - <scheme>:///abs/path         canonical absolute path
//   - <scheme>://~/relative        tilde via host="~"
//   - <scheme>://./relative        cwd-relative via host="."
//
// Sharing the resolver keeps the two schemes in lockstep — a
// new accepted form (or a fix to an existing one) lands in one
// place and applies to both.
package uriresolve

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupportedHost is returned when the URI's host slot is not
// one of "", "~", or "." — these are the only forms the local
// resolver accepts. Callers wrap this with their scheme name for
// a friendly error message.
var ErrUnsupportedHost = errors.New("uriresolve: unsupported host")

// ErrEmptyPath is returned when the URI has no path component
// after host resolution. Callers wrap with scheme context.
var ErrEmptyPath = errors.New("uriresolve: empty path")

// ResolveLocalPath turns a parsed URI into an absolute filesystem
// path according to the shared host conventions. Returns the
// resolved absolute path or one of the sentinel errors above
// (suitable for errors.Is checks).
//
// The returned path is always absolute — callers do not need to
// run filepath.Abs themselves.
func ResolveLocalPath(u *url.URL) (string, error) {
	var path string
	switch u.Host {
	case "":
		path = u.Path
	case "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(u.Path, "/"))
	case ".":
		path = "." + u.Path
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedHost, u.Host)
	}

	if path == "" {
		return "", ErrEmptyPath
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	return abs, nil
}

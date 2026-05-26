// Package uriresolve resolves the local-path form shared by the
// file:// and sqlite:// URI schemes.
//
// Both schemes accept the same shape:
//
//   - <scheme>:///abs/path         canonical absolute path
//
// Earlier revisions accepted file://~/path and file://./path as
// non-canonical aliases. Both abused the URI host slot to carry a
// relative-path prefix and produced subtly wrong behaviour when
// the path actually started with "~" or "." in regular characters.
// They were removed without deprecation in P1.12; bare paths remain
// the right way to write relative locations (DialDriver does its
// own home expansion on input that begins with "~/").
//
// Sharing the resolver between file:// and sqlite:// keeps the two
// schemes in lockstep — a new accepted form (or a fix to an existing
// one) lands in one place and applies to both.
package uriresolve

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
)

// ErrUnsupportedHost is returned when the URI's host slot is
// non-empty. After P1.12 the only accepted form is
// <scheme>:///abs/path (empty host); anything else is rejected.
// Callers wrap this with their scheme name for a friendly error
// message.
var ErrUnsupportedHost = errors.New("uriresolve: unsupported host")

// ErrEmptyPath is returned when the URI has no path component
// after host resolution. Callers wrap with scheme context.
var ErrEmptyPath = errors.New("uriresolve: empty path")

// ResolveLocalPath turns a parsed URI into an absolute filesystem
// path. Returns the resolved absolute path or one of the sentinel
// errors above (suitable for errors.Is checks).
//
// The returned path is always absolute — callers do not need to
// run filepath.Abs themselves.
func ResolveLocalPath(u *url.URL) (string, error) {
	if u.Host != "" {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedHost, u.Host)
	}
	if u.Path == "" {
		return "", ErrEmptyPath
	}

	abs, err := filepath.Abs(u.Path)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	return abs, nil
}

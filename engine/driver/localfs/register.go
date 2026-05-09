package localfs

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/rkurbatov/scrinium/engine/driver"
	"github.com/rkurbatov/scrinium/engine/internal/uriresolve"
)

// init registers the file:// scheme with the driver registry
// at package import. Importing driver/localfs is enough to
// activate the dialer; explicit calls aren't required.
func init() {
	driver.RegisterDialer("file", openFileURI)
}

// openFileURI builds a localfs Driver from a parsed file://
// URI. Forms accepted:
//
//   - file:///abs/path             canonical absolute path
//   - file://~/path                tilde expansion (host="~")
//   - file://./path                cwd-relative (host=".")
//
// The non-canonical forms abuse the URI's host slot to carry
// a relative-path prefix, but we accept them because writing
// relative paths in a URI any other way is awkward. Bare
// (non-URI) input handles relative paths more cleanly; this
// branch is for users who prefer to be explicit about the
// scheme in a URI form.
func openFileURI(u *url.URL) (driver.Driver, error) {
	abs, err := uriresolve.ResolveLocalPath(u)
	if err != nil {
		switch {
		case errors.Is(err, uriresolve.ErrUnsupportedHost):
			return nil, fmt.Errorf("localfs: file:// host %q not supported (use file:///path or file://~/path)", u.Host)
		case errors.Is(err, uriresolve.ErrEmptyPath):
			return nil, fmt.Errorf("localfs: file:// URI has empty path")
		default:
			return nil, fmt.Errorf("localfs: %w", err)
		}
	}
	return New(abs)
}

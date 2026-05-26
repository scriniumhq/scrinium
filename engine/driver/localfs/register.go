package localfs

import (
	"errors"
	"fmt"
	"net/url"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/uriresolve"
)

// init registers the file:// scheme with the driver registry
// at package import. Importing driver/localfs is enough to
// activate the dialer; explicit calls aren't required.
func init() {
	driver.RegisterDialer("file", openFileURI)
}

// openFileURI builds a localfs Driver from a parsed file://
// URI. The only accepted form is:
//
//   - file:///abs/path             canonical absolute path
//
// Earlier revisions accepted file://~/path and file://./path as
// non-canonical aliases. Both abused the URI host slot to carry a
// relative-path prefix and were removed in P1.12. Use bare paths
// (which DialDriver routes through home-directory expansion) for
// relative locations.
func openFileURI(u *url.URL) (driver.Driver, error) {
	abs, err := uriresolve.ResolveLocalPath(u)
	if err != nil {
		switch {
		case errors.Is(err, uriresolve.ErrUnsupportedHost):
			return nil, fmt.Errorf("localfs: file:// host %q not supported (use file:///path)", u.Host)
		case errors.Is(err, uriresolve.ErrEmptyPath):
			return nil, fmt.Errorf("localfs: file:// URI has empty path")
		default:
			return nil, fmt.Errorf("localfs: %w", err)
		}
	}
	return New(abs)
}

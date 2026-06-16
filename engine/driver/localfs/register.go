package localfs

import (
	"errors"
	"fmt"
	"net/url"

	"scrinium.dev/engine/driver"
	"scrinium.dev/internal/uri"
)

// init registers the file:// scheme with the driver registry
// at package import. Importing driver/localfs is enough to
// activate the dialer; explicit calls aren't required.
func init() {
	driver.RegisterDialer("file", openFileURI)
}

// openFileURI builds a localfs Driver from a parsed file://
// URI. Accepted forms (resolved by scrinium.dev/internal/uri,
// the resolver shared with sqlite:// and the assembler):
//
//   - file:///abs/path             canonical absolute path
//   - file://~/rel                 ~  → $HOME
//   - file://./rel                 .  → current directory
//
// Any other host (file://example.com/path) is rejected. The
// ~ and . aliases are resolved uniformly across every URI
// consumer, so file://~/x and the bare path ~/x mean the same.
func openFileURI(u *url.URL) (driver.Driver, error) {
	abs, err := uri.ResolveLocalPath(u)
	if err != nil {
		switch {
		case errors.Is(err, uri.ErrUnsupportedHost):
			return nil, fmt.Errorf("localfs: file:// host %q not supported (file:///abs, file://~/rel, file://./rel)", u.Host)
		case errors.Is(err, uri.ErrEmptyPath):
			return nil, fmt.Errorf("localfs: file:// URI has empty path")
		default:
			return nil, fmt.Errorf("localfs: %w", err)
		}
	}
	return New(abs)
}

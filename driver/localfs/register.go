package localfs

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rkurbatov/scrinium/driver"
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
	var path string
	switch u.Host {
	case "":
		// file:///abs/path → u.Path = "/abs/path".
		path = u.Path
	case "~":
		// file://~/relative → host="~", path="/relative".
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("localfs: expand ~: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(u.Path, "/"))
	case ".":
		// file://./relative → host=".", path="/relative".
		path = "." + u.Path
	default:
		// file://something/path — could be a non-localhost
		// authority (we don't support remote file:// in
		// localfs) or an unusual rooted form. Reject so
		// confusing inputs fail fast.
		return nil, fmt.Errorf("localfs: file:// host %q not supported (use file:///path or file://~/path)", u.Host)
	}

	if path == "" {
		return nil, fmt.Errorf("localfs: file:// URI has empty path")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("localfs: absolute path: %w", err)
	}
	return New(abs)
}

package driver

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Dialer opens a Driver from a parsed URI. Implementations
// live in scheme-specific packages and register themselves
// via RegisterDialer in their package init().
//
// The URI is already parsed by DialDriver — the dialer
// receives the *url.URL and is responsible for translating
// scheme-specific parts (host, path, query) into driver
// configuration.
type Dialer func(u *url.URL) (Driver, error)

// dialers holds the registered URI scheme handlers. Populated
// by package init() in driver/<scheme> packages, read by
// DialDriver. The mutex guards registration vs lookup; once
// init() phase is over, lookups are read-only and uncontended.
var (
	dialersMu sync.RWMutex
	dialers   = map[string]Dialer{}
)

// RegisterDialer attaches a Dialer to a URI scheme. Called
// from package init() in driver implementations:
//
//	// driver/localfs/register.go
//	func init() { driver.RegisterDialer("file", openFileURI) }
//
// Re-registering the same scheme overwrites the previous
// dialer and panics — this catches accidental double imports
// or scheme collisions at startup, before any user URIs are
// dialled.
func RegisterDialer(scheme string, d Dialer) {
	dialersMu.Lock()
	defer dialersMu.Unlock()
	if _, exists := dialers[scheme]; exists {
		panic(fmt.Sprintf("driver: dialer for scheme %q already registered", scheme))
	}
	dialers[scheme] = d
}

// RegisteredSchemes returns the schemes currently registered.
// Sorted for deterministic output; useful in error messages
// and in --help output of binaries.
func RegisteredSchemes() []string {
	dialersMu.RLock()
	defer dialersMu.RUnlock()
	out := make([]string, 0, len(dialers))
	for s := range dialers {
		out = append(out, s)
	}
	sortStrings(out)
	return out
}

// DialDriver opens a Driver by URI. The scheme selects the
// implementation (registered via RegisterDialer); the rest
// of the URI is forwarded to the dialer.
//
// Bare paths without a scheme — "/abs/path", "./relative",
// or "~/something" — are treated as file:// for backward
// compatibility with configs that pre-date URI support and
// for ergonomics on the CLI. Requires the file:// dialer
// to be registered (it is, by importing driver/localfs).
//
// Returned errors:
//   - empty URI                       → "empty URI"
//   - URL parse failure               → wrapped url.Parse error
//   - unregistered scheme             → "scheme X not registered"
//   - dialer-specific failures        → wrapped from the dialer
func DialDriver(uri string) (Driver, error) {
	if uri == "" {
		return nil, fmt.Errorf("driver: empty URI")
	}

	// Bare paths bypass URL parsing and route through file://.
	if !looksLikeURI(uri) {
		path, err := expandPath(uri)
		if err != nil {
			return nil, fmt.Errorf("driver: %w", err)
		}
		return dialBarePath(path)
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("driver: parse %q: %w", uri, err)
	}

	dialersMu.RLock()
	d, ok := dialers[u.Scheme]
	dialersMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("driver: scheme %q not registered (import driver/%s to enable; available: %v)",
			u.Scheme, u.Scheme, RegisteredSchemes())
	}
	return d(u)
}

// dialBarePath synthesises a file:// URL out of a bare path
// and dispatches through the registry. Centralising this
// rather than calling localfs.New directly keeps the dial
// path uniform — every Driver creation goes through the
// registered file dialer, even on the legacy code path.
func dialBarePath(absPath string) (Driver, error) {
	dialersMu.RLock()
	d, ok := dialers["file"]
	dialersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver: bare path requires the file:// scheme but it is not registered (import driver/localfs)")
	}
	u := &url.URL{Scheme: "file", Path: absPath}
	return d(u)
}

// looksLikeURI reports whether s starts with what a URI parser
// would recognise as a scheme: alpha character followed by
// alphanumerics / "+-." / ":" then "://".
//
// Implemented manually rather than calling url.Parse to keep
// classification independent of parser quirks (url.Parse will
// happily accept "/path" and report Scheme="", which we want
// to keep distinct from a true URI without a scheme).
func looksLikeURI(s string) bool {
	if len(s) < 4 { // shortest possible: "a://"
		return false
	}
	if !isAlpha(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case isAlpha(c), c >= '0' && c <= '9', c == '+', c == '-', c == '.':
			continue
		case c == ':':
			return strings.HasPrefix(s[i:], "://")
		default:
			return false
		}
	}
	return false
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// expandPath does ~/ tilde expansion and resolves to an
// absolute path. Used for bare-path inputs to DialDriver.
func expandPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		p = filepath.Join(home, p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	return abs, nil
}

// sortStrings is an in-place ascending sort. We avoid pulling
// "sort" into this file because the only caller is
// RegisteredSchemes which itself runs once or twice per
// process; insertion sort over <10 elements is faster and
// keeps imports tight.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

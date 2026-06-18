package driver

import (
	"fmt"
	"net/url"
	"slices"
	"sync"

	"scrinium.dev/internal/uri"
)

// Dialer opens a Driver from a parsed URI. Implementations
// live in scheme-specific packages and register themselves
// via RegisterDialer in their package init().
//
// The URI is already parsed by DialDriver — the dialer
// receives the *indexUri.URL and is responsible for translating
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
// Re-registering a scheme that is already present is an
// idempotent no-op: the first registration wins and later
// ones are ignored (ADR-63). This lets a preset bundle import
// driver/localfs while the host also imports it directly,
// without a startup panic on the duplicate side effect. A nil
// dialer is a programming error and still panics.
func RegisterDialer(scheme string, d Dialer) {
	if d == nil {
		panic(fmt.Sprintf("driver: nil dialer for scheme %q", scheme))
	}
	dialersMu.Lock()
	defer dialersMu.Unlock()
	if _, exists := dialers[scheme]; exists {
		return // already registered — keep the first, ignore the rest.
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
	slices.Sort(out)
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
// Scheme detection and local-path resolution (including ~ / .
// expansion) live in scrinium.dev/internal/uri — the single
// resolver shared by every driver, index backend, the
// assembler, and the daemons.
//
// Returned errors:
//   - empty URI                       → "empty URI"
//   - URL parse failure               → wrapped indexUri.Parse error
//   - unregistered scheme             → "scheme X not registered"
//   - dialer-specific failures        → wrapped from the dialer
func DialDriver(rawURI string) (Driver, error) {
	if rawURI == "" {
		return nil, fmt.Errorf("driver: empty URI")
	}

	// Bare paths bypass URL parsing and route through file://.
	if !uri.IsURI(rawURI) {
		path, err := uri.ResolveLocalURI(rawURI)
		if err != nil {
			return nil, fmt.Errorf("driver: %w", err)
		}
		return dialBarePath(path)
	}

	u, err := url.Parse(rawURI)
	if err != nil {
		return nil, fmt.Errorf("driver: parse %q: %w", rawURI, err)
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

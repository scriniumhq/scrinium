package driver

import (
	"context"
	"fmt"
	"net/url"

	"scrinium.dev/internal/registry"
	"scrinium.dev/internal/uri"
)

// Dialer opens a Driver from a parsed URI. Implementations
// live in scheme-specific packages and register themselves
// via RegisterDialer in their package init().
//
// The URI is already parsed by DialDriver — the dialer
// receives the *url.URL and is responsible for translating
// scheme-specific parts (host, path, query) into driver
// configuration.
type Dialer func(ctx context.Context, u *url.URL, opts ...DialOption) (Driver, error)

// dialers holds the registered URI scheme handlers. Populated
// by package init() in driver/<scheme> packages, read by
// DialDriver. The registry guards registration vs lookup; once
// the init() phase is over, lookups are read-only and uncontended.
var dialers = registry.New[Dialer]()

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
	dialers.SetFirstWins(scheme, d) // first wins; duplicates ignored (ADR-63).
}

// RegisteredSchemes returns the schemes currently registered.
// Sorted for deterministic output; useful in error messages
// and in --help output of binaries.
func RegisteredSchemes() []string {
	return dialers.Keys()
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
//   - URL parse failure               → wrapped url.Parse error
//   - unregistered scheme             → "scheme X not registered"
//   - dialer-specific failures        → wrapped from the dialer
func DialDriver(ctx context.Context, rawURI string, opts ...DialOption) (Driver, error) {
	if rawURI == "" {
		return nil, fmt.Errorf("driver: empty URI")
	}

	// Bare paths bypass URL parsing and route through file://.
	if !uri.IsURI(rawURI) {
		path, err := uri.ResolveLocalURI(rawURI)
		if err != nil {
			return nil, fmt.Errorf("driver: %w", err)
		}
		return dialBarePath(ctx, path, opts...)
	}

	u, err := url.Parse(rawURI)
	if err != nil {
		return nil, fmt.Errorf("driver: parse %q: %w", rawURI, err)
	}

	d, ok := dialers.Get(u.Scheme)
	if !ok {
		return nil, fmt.Errorf("driver: scheme %q not registered (import driver/%s to enable; available: %v)",
			u.Scheme, u.Scheme, RegisteredSchemes())
	}
	return d(ctx, u, opts...)
}

// dialBarePath synthesises a file:// URL out of a bare path
// and dispatches through the registry. Centralising this
// rather than calling localfs.New directly keeps the dial
// path uniform — every Driver creation goes through the
// registered file dialer.
func dialBarePath(ctx context.Context, absPath string, opts ...DialOption) (Driver, error) {
	d, ok := dialers.Get("file")
	if !ok {
		return nil, fmt.Errorf("driver: bare path requires the file:// scheme but it is not registered (import driver/localfs)")
	}
	u := &url.URL{Scheme: "file", Path: absPath}
	return d(ctx, u, opts...)
}

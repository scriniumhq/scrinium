package index

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"
)

// Dialer opens a StoreIndex from a parsed URI. Implementations
// live in scheme-specific packages (index/sqlite, eventually
// index/postgres) and register themselves via RegisterDialer
// in their package init().
//
// IndexOption values are forwarded so callers can pass cross-
// backend tunables (e.g. a Publisher) while leaving backend-
// specific tuning to URI query parameters or backend-specific
// options.
type Dialer func(ctx context.Context, u *url.URL, opts ...IndexOption) (StoreIndex, error)

// dialers holds the registered URI scheme handlers. Populated
// by package init() in index/<scheme> packages, read by
// DialIndex.
var (
	dialersMu sync.RWMutex
	dialers   = map[string]Dialer{}
)

// RegisterDialer attaches a Dialer to a URI scheme. Called
// from package init() in index implementations:
//
//	// index/sqlite/register.go
//	func init() { index.RegisterDialer("sqlite", openSQLiteURI) }
//
// Re-registering a scheme that is already present is an
// idempotent no-op: the first registration wins and later
// ones are ignored (ADR-63), so a preset bundle and a host
// can both import index/sqlite without a startup panic on the
// duplicate side effect. A nil dialer is a programming error
// and still panics.
func RegisterDialer(scheme string, d Dialer) {
	if d == nil {
		panic(fmt.Sprintf("index: nil dialer for scheme %q", scheme))
	}
	dialersMu.Lock()
	defer dialersMu.Unlock()
	if _, exists := dialers[scheme]; exists {
		return // already registered — keep the first, ignore the rest.
	}
	dialers[scheme] = d
}

// RegisteredSchemes returns the schemes currently registered.
// Sorted; useful for error messages and --help output.
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

// DialIndex opens a StoreIndex by URI. The scheme selects the
// backend (registered via RegisterDialer); the rest of the URI
// is forwarded to the dialer.
//
// Unlike DialDriver, there is no bare-path fallback: index
// URIs are new from day one (no legacy "indexPath" config to
// honour), and a bare path is ambiguous between sqlite, file
// store, etc. The scheme is mandatory.
func DialIndex(ctx context.Context, uri string, opts ...IndexOption) (StoreIndex, error) {
	if uri == "" {
		return nil, fmt.Errorf("index: empty URI")
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("index: parse %q: %w", uri, err)
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("index: URI %q has no scheme (expected e.g. sqlite://path)", uri)
	}

	dialersMu.RLock()
	d, ok := dialers[u.Scheme]
	dialersMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("index: scheme %q not registered (import index/%s to enable; available: %v)",
			u.Scheme, u.Scheme, RegisteredSchemes())
	}
	return d(ctx, u, opts...)
}

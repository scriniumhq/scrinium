package secretref

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Resolver turns the value part of a SecretRef (everything after the
// first colon) into raw secret bytes. It receives the load context so
// network-backed schemes can honour cancellation.
type Resolver func(ctx context.Context, value string) ([]byte, error)

var (
	mu        sync.RWMutex
	resolvers = map[string]Resolver{
		"file":  resolveFile,
		"env":   resolveEnv,
		"plain": resolvePlain,
	}
)

// Register installs a Resolver for scheme. It panics if scheme is
// empty or already registered, so a typo or a double import surfaces
// at startup rather than silently shadowing a built-in. Call from an
// init().
func Register(scheme string, r Resolver) {
	if scheme == "" {
		panic("secretref: empty scheme")
	}
	if r == nil {
		panic("secretref: nil resolver for scheme " + scheme)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := resolvers[scheme]; dup {
		panic("secretref: scheme already registered: " + scheme)
	}
	resolvers[scheme] = r
}

// Ref is a raw, unresolved "<scheme>:<value>" reference as it appears
// in the config. The zero Ref means "unset".
type Ref string

// IsZero reports whether the ref was omitted in the config.
func (r Ref) IsZero() bool { return r == "" }

// String masks the value so a Ref logged or printed never leaks the
// secret — only the scheme and a fixed redaction marker show.
func (r Ref) String() string {
	if r == "" {
		return ""
	}
	if scheme, _, ok := split(string(r)); ok {
		return scheme + ":<redacted>"
	}
	return "<redacted>"
}

// MarshalYAML masks the secret when a config is serialised back out
// (composer.Explain). A round-tripped config therefore cannot leak a
// passphrase or credential; Explain is for inspection, not re-loading.
func (r Ref) MarshalYAML() (any, error) { return r.String(), nil }

// MarshalJSON masks the secret for the same reason as MarshalYAML.
func (r Ref) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(r.String())), nil
}

// Resolve splits ref on its first colon, looks up the scheme, and
// invokes its resolver. A ref without a scheme, or with an unknown
// scheme, is an error: there is no implicit default, so a bare path
// pasted where a SecretRef belongs fails loudly instead of being
// read as a literal secret.
func (r Ref) Resolve(ctx context.Context) ([]byte, error) {
	if r == "" {
		return nil, fmt.Errorf("secretref: empty reference")
	}
	scheme, value, ok := split(string(r))
	if !ok {
		return nil, fmt.Errorf("secretref %q: missing \"<scheme>:\" prefix", r.String())
	}
	mu.RLock()
	resolve, known := resolvers[scheme]
	mu.RUnlock()
	if !known {
		return nil, fmt.Errorf("secretref: unknown scheme %q", scheme)
	}
	b, err := resolve(ctx, value)
	if err != nil {
		return nil, fmt.Errorf("secretref %q: %w", r.String(), err)
	}
	return b, nil
}

// split divides "scheme:value" at the first colon. ok is false when
// there is no colon at all.
func split(s string) (scheme, value string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func resolveFile(_ context.Context, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("file: empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(string(b), " \t\r\n")), nil
}

func resolveEnv(_ context.Context, name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("env: empty variable name")
	}
	v := os.Getenv(name)
	if v == "" {
		return nil, fmt.Errorf("env: %s is unset or empty", name)
	}
	return []byte(v), nil
}

func resolvePlain(_ context.Context, value string) ([]byte, error) {
	// plain: is intentionally permissive (empty is allowed for the
	// rare "empty passphrase" test) and never touches the
	// environment or filesystem. Masked in logs via Ref.String.
	return []byte(value), nil
}

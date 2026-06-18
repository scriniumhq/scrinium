// Package uri is the single place the whole library turns a store/index
// URI — or a bare filesystem path — into a scheme and an absolute local
// path. Every driver, index backend, the assembler, and the daemons go
// through it, so URI handling stays in lockstep and there are no divergent
// copies of "what does file://~/x mean".
//
// Accepted local forms (file:// and the scheme-less bare path share them):
//
//   - file:///abs/path   canonical absolute path (empty host)
//   - file://~/rel        ~  → $HOME            (home-relative)
//   - file://./rel        .  → current dir      (cwd-relative)
//   - ~/rel, ./rel, /abs  bare paths, same ~ / . / abs rules
//
// The ~ and . host aliases are deliberately supported here (a single
// resolver makes the tilde behaviour uniform across file:// and the bare
// path, which is what hosts expect). Any other host (file://example.com/…)
// is ErrUnsupportedHost.
package uri

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupportedHost — a file-like URI carried a host other than the
// empty host or the "~" / "." aliases (e.g. file://example.com/path).
var ErrUnsupportedHost = errors.New("uri: unsupported host")

// ErrEmptyPath — the URI resolved to no path at all (e.g. "file://").
var ErrEmptyPath = errors.New("uri: empty path")

// ErrNotLocal — ResolveLocalURI was handed a URI whose scheme is not
// file:// (e.g. s3://, postgres://). Callers that only need a local
// directory check for it and skip cleanly.
var ErrNotLocal = errors.New("uri: not a local path")

// SchemeOf returns the URI scheme ("file", "sqlite", "s3", …) or "" for a
// bare path. It is the one scheme extractor in the library; registries and
// dialers classify by it instead of hand-rolling "://" scans.
func SchemeOf(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Scheme
}

// IsURI reports whether s carries a scheme (SchemeOf(s) != ""). A bare path
// — "/abs", "./rel", "~/x", "rel" — is not a URI.
func IsURI(s string) bool { return SchemeOf(s) != "" }

// ResolveLocalPath turns a parsed file-like URL into an absolute local
// path, expanding the ~ and . host aliases. The result is always
// absolute — callers need not run filepath.Abs themselves.
func ResolveLocalPath(u *url.URL) (string, error) {
	var p string
	switch u.Host {
	case "":
		p = u.Path
	case "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("uri: expand ~: %w", err)
		}
		p = filepath.Join(home, strings.TrimPrefix(u.Path, "/"))
	case ".":
		p = "." + u.Path
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedHost, u.Host)
	}
	if p == "" {
		return "", ErrEmptyPath
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("uri: absolute path: %w", err)
	}
	return abs, nil
}

// ResolveLocalURI resolves a raw store/index URI string to an absolute
// local path. A bare path ("~/x", "./rel", "/abs") expands ~ then Abs; a
// file:// URI goes through ResolveLocalPath; any other scheme yields
// ErrNotLocal. It is what the assembler uses to find the store directory
// and what DialDriver uses for its bare-path branch.
func ResolveLocalURI(s string) (string, error) {
	if s == "" {
		return "", ErrEmptyPath
	}
	if !IsURI(s) {
		return expandBare(s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("uri: parse %q: %w", s, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("%w: scheme %q", ErrNotLocal, u.Scheme)
	}
	return ResolveLocalPath(u)
}

// expandBare resolves a scheme-less path: ~ / ~/ expand to $HOME, then the
// result is made absolute (so "./rel" and "rel" resolve against the cwd).
func expandBare(p string) (string, error) {
	switch {
	case p == "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("uri: expand ~: %w", err)
		}
		p = home
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("uri: expand ~: %w", err)
		}
		p = filepath.Join(home, p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("uri: absolute path: %w", err)
	}
	return abs, nil
}

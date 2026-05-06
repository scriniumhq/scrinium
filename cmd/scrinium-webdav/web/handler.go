package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// BackingFS is the small file-system surface web needs from its
// host. The daemon implements it on top of webdavFS, but the
// shape is generic: any FS that can Stat and OpenFile fits.
//
// Interface is intentionally narrow — adding methods means more
// surface to mock in tests and more contract risk. New abilities
// (e.g. artifact-by-id lookup in phase 2) get their own
// interface, kept orthogonal.
type BackingFS interface {
	// Stat returns the FileInfo for path. cleanWebDAVPath-style
	// normalisation is the host's responsibility before calling.
	Stat(ctx context.Context, name string) (os.FileInfo, error)

	// OpenFile opens a path for reading. Files: returned handle
	// streams bytes (web uses http.ServeContent over it).
	// Directories: handle's Readdir returns FileInfo for each
	// child (the same contract webdav.File enforces).
	OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (File, error)
}

// File is the read+listing surface web requires of a backing
// file handle. Mirrors webdav.File minus write methods we never
// invoke (Truncate, Seek-on-write).
type File interface {
	Read(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
	Readdir(count int) ([]os.FileInfo, error)
	Stat() (os.FileInfo, error)
}

// PathCleaner normalises a URL path the way the host wants
// before any FS lookup. The daemon plugs in cleanWebDAVPath
// (the same helper WebDAV uses) so web sees identical
// normalisation in both surfaces.
type PathCleaner func(string) string

// Config holds the daemon settings web needs. Mirrors the
// fields cmd's Config exposes; we redeclare here (rather than
// import the cmd) so web stays a clean library that any
// daemon can reuse.
type Config struct {
	StorePath     string // displayed in the page header
	ServicePrefix string // for the "stats" footer link, e.g. "_scrinium"
	BrowsePrefix  string // the URL prefix this handler is mounted under
}

// Handler is the http.Handler that renders web pages. Construct
// with NewHandler; mount under cfg.BrowsePrefix in the daemon's
// top-level mux.
type Handler struct {
	fs    BackingFS
	clean PathCleaner
	cfg   Config

	// prefix is the normalised mount prefix: leading slash
	// guaranteed, trailing slash stripped. Used by every link
	// builder so navigation stays inside the browser surface.
	prefix string
}

// NewHandler builds a Handler. Empty BrowsePrefix is rejected —
// the daemon should not call this when the browser is disabled.
func NewHandler(fs BackingFS, clean PathCleaner, cfg Config) *Handler {
	prefix := strings.TrimRight(cfg.BrowsePrefix, "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return &Handler{
		fs:     fs,
		clean:  clean,
		cfg:    cfg,
		prefix: prefix,
	}
}

// Prefix returns the normalised URL prefix (leading slash, no
// trailing slash). Useful for daemons setting up the mux.
func (h *Handler) Prefix() string { return h.prefix }

// ServeHTTP routes within the browser surface. Today only
// directory listings and file streaming exist; phases 2 and 3
// add /_artifact/<id> and /_stats branches before falling
// through to the listing/file handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Strip the configured prefix from the URL path. The mux
	// gives us a path that begins with prefix; we need the
	// store-relative remainder for FS lookups.
	rel := strings.TrimPrefix(r.URL.Path, h.prefix)
	rel = strings.TrimPrefix(rel, "/")
	clean := rel
	if h.clean != nil {
		clean = h.clean(rel)
	}

	fi, err := h.fs.Stat(r.Context(), clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !fi.IsDir() {
		h.serveFile(w, r, clean, fi)
		return
	}

	h.serveListing(w, r, clean)
}

// serveFile streams a single file. Uses http.ServeContent for
// proper Range/Last-Modified support — the underlying File is
// io.ReadSeeker by contract.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string, fi os.FileInfo) {
	f, err := h.fs.OpenFile(r.Context(), name, os.O_RDONLY, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// serveError writes an HTML error page. Used by listing/artifact
// handlers when the underlying call returns an error worth
// surfacing to the user (as opposed to "not found", which goes
// through http.NotFound).
func (h *Handler) serveError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, fmt.Sprintf("scrinium-web: %s", msg), status)
}

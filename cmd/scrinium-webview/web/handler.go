package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"scrinium.dev/engine/domain"
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

	// LookupManifest returns the full domain.Manifest for the
	// given artifact id, used by the artifact details page.
	// (m, true, nil) — found. (zero, false, nil) — no such
	// artifact. The third return covers infrastructure errors
	// (driver unavailable, decode failure).
	LookupManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, bool, error)

	// OpenArtifact opens the bytes of an artifact by id. Used
	// by the /_view/<id> and /_download/<id> endpoints which
	// don't have a path to OpenFile against.
	//
	// Returns the read handle, the artifact's display name
	// (typically the fsmeta path's basename, for
	// Content-Disposition), and a best-effort MIME type.
	// (nil, _, _, err) when the artifact doesn't exist or
	// can't be opened.
	OpenArtifact(ctx context.Context, id domain.ArtifactID) (File, ArtifactMeta, error)

	// LookupRelated returns every artifact that shares the
	// given BlobRef, excluding the one identified by
	// `exclude` (typically the artifact being viewed). Used
	// by the artifact details page to surface dedup siblings
	// — the "where else does this blob live" question that's
	// distinctive to a content-addressable store.
	//
	// Returns a fresh slice on every call; nil when no
	// siblings exist.
	LookupRelated(ctx context.Context, blobRef domain.BlobRef, exclude domain.ArtifactID) ([]RelatedArtifact, error)

	// Search returns artifacts whose path or namespace
	// contains the query as a substring (case-insensitive),
	// or whose id matches exactly. limit caps the response;
	// a value of 0 means unlimited. Used by the /_search
	// endpoint.
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)

	// LookupLocations returns the per-tree paths of an
	// artifact. Empty fields signal "this tree doesn't
	// carry it" (e.g. PathByPath="" for orphaned artifacts).
	// Used by the artifact details page's Locations panel
	// to wire "show me where this lives" links.
	LookupLocations(ctx context.Context, id domain.ArtifactID) (Locations, bool, error)
}

// Locations mirrors projection.Locations.
type Locations struct {
	ByArtifact  string
	BySession   string
	ByNamespace string
	ByDate      string
	ByPath      string
	ByOrphaned  string
}

// SearchResult mirrors projection.SearchResult — kept here so
// web stays a clean library hosts adapt to.
type SearchResult struct {
	ArtifactID  domain.ArtifactID
	Path        string
	Namespace   string
	SessionID   domain.SessionID
	CreatedAt   time.Time
	MIME        string
	MatchReason string // "path" | "namespace" | "id"
}

// RelatedArtifact mirrors projection.RelatedArtifact verbatim,
// kept here so web stays a clean library that hosts adapt to
// rather than importing projection. Hosts translate at the
// boundary; the few extra fields don't justify a shared
// package.
type RelatedArtifact struct {
	ArtifactID domain.ArtifactID
	Path       string // by-path placement; empty if orphaned
	Namespace  string
	SessionID  domain.SessionID
	CreatedAt  time.Time
}

// ArtifactMeta is the small descriptor returned alongside the
// File handle by OpenArtifact. Carries the fields web needs to
// build proper response headers without re-deriving them per
// route.
type ArtifactMeta struct {
	Name    string // for Content-Disposition; "" → fall back to id
	MIME    string // best-effort; empty → http.DetectContentType
	ModTime time.Time
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

	// decoders maps a Manifest.Usr "kind" marker to the
	// SchemaDecoder that knows how to render it. nil until the
	// host calls RegisterDecoder. Lookup on the request hot
	// path is cheap (small map, no contention).
	decoders map[string]SchemaDecoder

	// statsProvider, when set, supplies the live data for the
	// /_stats HTML page. nil disables the page (404). The
	// host typically sets one at boot via SetStatsProvider.
	statsProvider StatsProvider
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
// directory listings, file streaming, and artifact details
// pages exist; phase 3 will add /_stats before the listing
// fall-through.
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

	// Internal sub-routes use the "_" prefix at the top level
	// of the browser space — collisions with real content are
	// impossible because user paths can't start with "_" at
	// the FS root (the WebDAV layer rejects them).
	if id, ok := matchArtifactRoute(rel); ok {
		h.serveArtifact(w, r, id)
		return
	}
	if id, ok := matchByIDRoute(rel, "_view/"); ok {
		h.serveByID(w, r, id, dispositionInline)
		return
	}
	if id, ok := matchByIDRoute(rel, "_download/"); ok {
		h.serveByID(w, r, id, dispositionAttachment)
		return
	}
	if rel == "_stats" || rel == "_stats/" {
		h.serveStats(w, r)
		return
	}
	if rel == "_search" || rel == "_search/" {
		h.serveSearch(w, r)
		return
	}

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

// matchArtifactRoute parses paths of the form "_artifact/<id>"
// at the top of the browser-relative path. Returns the id and
// true when matched. The id may contain any character except
// "/" (we accept the rest of the segment verbatim — domain ids
// may use hyphens, dashes, even slashes once we adopt
// hierarchical schemes; for now they're flat strings).
func matchArtifactRoute(rel string) (domain.ArtifactID, bool) {
	return matchByIDRoute(rel, "_artifact/")
}

// matchByIDRoute parses paths of the form "<prefix><id>" at
// the top of the browser-relative path. Generic over the
// _artifact / _view / _download routes which share the shape.
func matchByIDRoute(rel, prefix string) (domain.ArtifactID, bool) {
	if !strings.HasPrefix(rel, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(rel, prefix)
	if id == "" {
		return "", false
	}
	// Reject sub-paths under <prefix><id>/... — these routes
	// each address a single resource, not a tree.
	if strings.Contains(id, "/") {
		return "", false
	}
	return domain.ArtifactID(id), true
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

// disposition selects the Content-Disposition mode for
// serveByID. Inline is used for /_view, attachment for
// /_download.
type disposition int

const (
	dispositionInline disposition = iota
	dispositionAttachment
)

// serveByID streams the bytes of an artifact identified by id.
// Both /_view and /_download go through here; they differ only
// in the Content-Disposition header. http.ServeContent handles
// Range requests and Last-Modified — we wire ModTime from the
// metadata so cache validation works.
func (h *Handler) serveByID(w http.ResponseWriter, r *http.Request, id domain.ArtifactID, mode disposition) {
	f, meta, err := h.fs.OpenArtifact(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	name := meta.Name
	if name == "" {
		name = string(id)
	}

	// Set Content-Type explicitly when the host gave us one.
	// http.ServeContent will sniff if we leave it empty; that
	// fallback is fine for unknown blobs.
	if meta.MIME != "" {
		w.Header().Set("Content-Type", meta.MIME)
	}

	switch mode {
	case dispositionInline:
		// Browser-renderable types only. The listing only
		// links to /_view for whitelisted MIMEs, but a user
		// hitting the URL directly for a non-renderable
		// artifact still gets the bytes — just the browser
		// will likely save-as instead of showing inline.
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("inline; filename=%q", name))
	case dispositionAttachment:
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", name))
	}

	http.ServeContent(w, r, name, meta.ModTime, f)
}

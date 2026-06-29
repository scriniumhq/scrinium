// Package web is the secondary, human-facing surface of the
// scrinium-webdav daemon: HTML directory listings, per-artifact
// detail pages, and a stats view. WebDAV — the primary surface —
// stays at the daemon's root path, undisturbed; web is mounted
// under cfg.BrowsePrefix (default "/_browse") as a sibling
// handler in the same mux.
//
// The package decouples HTML rendering from WebDAV plumbing.
// It never speaks WebDAV — it only consumes a small FS-shaped
// interface (BackingFS below) that the daemon implements on
// top of webdavFS. Schema-aware rendering of artifact metadata
// comes from the extensions' present.SchemaPresenter capability
// (ADR-109), handed in by the daemon (SetPresenters) so the web
// pkg itself stays schema-agnostic.
//
// The split happened in the docs/web-fase-1 milestone — earlier
// versions kept the listing in the cmd top-level browser.go.
// Splitting allows phase 2 (artifact pages) and phase 3 (HTML
// stats) to grow without churning the cmd layer.
package web

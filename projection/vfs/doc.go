// Package vfs is the read/write virtual filesystem layer
// over a view.View.
//
// The View tracks artifact placements in several trees
// (by-path, by-date, by-namespace, by-session, by-artifact,
// orphaned). VFS turns those trees into a uniform Stat /
// OpenFile / Readdir surface, plus the optional service
// trees rooted at a configurable prefix (default
// "_scrinium/...").
//
// VFS is the substrate every Scrinium surface stands on:
//
//   - scrinium-fuse    — wraps VFS as a FUSE filesystem.
//   - scrinium-webdav  — wraps VFS as a webdav.FileSystem.
//   - scrinium-webview — reads through VFS to render HTML.
//
// External users building admin panels, agents, or other
// integrations consume VFS the same way: open a Daemon (which
// owns the View), construct a VFS over it, and translate
// Stat/OpenFile to whatever surface they want.
//
// Editing semantics live with the VFS — Mkdir, RemoveAll,
// Rename, OpenFile-with-O_CREATE — gated by the FSOps editing
// policy a host configures. Read-only callers (webview)
// simply never invoke the write methods; an explicit
// EditingOff policy is recommended belt-and-braces.
//
// Service trees are opt-in. A host that wants only the
// rootView surface (FUSE for production data drop, WebDAV
// for backup) leaves all Show* fields false. A diagnostic
// surface (webview) turns them all on. Visibility is
// per-tree so each surface picks its own balance.
package vfs

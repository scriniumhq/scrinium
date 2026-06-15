// Command scrinium-webdav exposes a Scrinium store over WebDAV —
// cross-platform access (Finder, Windows Explorer, rclone) without a
// kernel custom index or root.
//
// The store/projection is described by a Scrinium configuration document; how
// it is served (listen address, OS-junk filtering) is given by flags.
// This split is deliberate: the config says WHAT is stored and how it
// is projected, the daemon decides HOW to expose it.
//
//	scrinium-webdav serve --config store.yaml --listen :8080
//
// This file is a reference implementation. It is intentionally small
// and self-contained: copy the package and adapt it to wrap a Scrinium
// store in your own service. The reusable parts live in scrinium
// (assembly) and engine/projection; everything here is glue you are
// meant to own.
package main

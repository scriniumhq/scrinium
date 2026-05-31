// Command scrinium-webview serves a read-only HTML browser over a
// Scrinium store — listings, artifact detail pages, previews, search
// and a stats dashboard. Cross-platform; mutations belong on
// scrinium-webdav / scrinium-fuse.
//
// The store/projection is described by a Scrinium configuration document; the
// listen address and URL shaping are flags.
//
//	scrinium-webview serve --config store.yaml --listen :8081
//
// This file is a reference implementation, and webview is the most
// likely of the three to be customised: the rendering lives in the
// sibling web/ package (templates, listing, artifact, preview pages),
// the data adapter in webfs.go/stats_data.go/decoders.go. To restyle
// or reshape the UI, copy this command and edit web/ — the data side
// (the BackingFS adapter) stays as is. The store assembly itself
// (scrinium) is never touched.
package main

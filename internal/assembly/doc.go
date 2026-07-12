// Package assembly is the engine-internal assembler behind the public
// scrinium facade. It turns a Config — the declarative model owned by
// package config (scrinium.dev/config), parsed there and validated
// there — into a live, fully assembled Scrinium stack: store, index,
// projection view, and the read/write FSOps facade, ready to use. The
// assembly's own job is wiring components; the configuration model
// (shape, defaults, validation, policy mapping) is not here.
//
// It deliberately has no Serve/Run loop: the assembler's job ends at
// assembling the stack; how it is exposed (mounted via FUSE, served over
// WebDAV, browsed over HTTP) is the concern of the adapter program that
// holds the Assembly. An adapter reads the accessors it needs and drives
// its own server or mount with its own lifecycle. This keeps the
// assembler focused on "what is stored and how it is projected" and
// leaves "how it is served" to the adapter.
//
// The package is internal: applications go through the scrinium facade
// (scrinium.Open / scrinium.Build / scrinium.LoadYAML), which wraps an
// Assembly in a *scrinium.ScriniumClient.
package assembly

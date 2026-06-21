// Package projection builds virtual filesystem-like views over the
// flat content-addressed store. The package is the seam at which
// transport-specific daemons (cmd/scrinium-fuse, cmd/scrinium-webdav)
// plug in: projection itself does no syscalls and no networking.
//
// Architecture: View is the in-memory tree (read side) populated by
// backfill from a source.Provider. FSOps adds the write side —
// create/unlink/rename/setattr — and is the place where scratch
// buffering, path-level locks and editing policies live. Together
// they cover ~80% of what FUSE and WebDAV daemons need; the
// transport layer is a thin dispatcher.
//
// Schemas describing how artifacts map to filesystem paths live in
// subpackages (domain/vfsmeta is the standard one). They are
// pluggable through the source.Resolver function passed to view.New.
//
// Specification: docs/3 §5 Projection API, docs/4 §13 Projection,
// docs/4 §14 FUSE Mount.
package projection

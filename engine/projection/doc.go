// Package projection builds virtual human-friendly views over the
// store without copying data. A read-only alternative to searching
// by namespace/SessionID for cases when the natural mental model is
// a hierarchy (by path, by session, by type).
//
// A View is a snapshot of the state at the moment it was created.
// Live updates are on the backlog. The data source can be either a
// single core.DataStore or a curator.Curator: in the first case
// StorageFacet remains nil; in the second it is filled from
// MultistoreIndex and HostStorage.
//
// Mounting (FUSE, WebDAV) ships as separate integrations gated by
// the build tags `fuse` and `webdav`. Without the tags the API is
// available (so development stays transparent) but returns
// ErrFUSENotSupported / ErrWebDAVNotSupported.
//
// DAG: projection imports core, event. It does not import curator
// (the dependency is inverted via ProjectionSource), agent, or
// maintenance.
//
// Implementation lands in M6. In M0 — the type contracts.
package projection

// Package projection builds virtual filesystem-like views over the
// flat content-addressed store. The package is the seam at which
// transport-specific daemons (cmd/scrinium-fuse, cmd/scrinium-webdav)
// plug in: projection itself does no syscalls and no networking.
//
// Architecture: View is the in-memory tree (read side) populated by
// backfill from a ProjectionSource. FSOps adds the write side —
// create/unlink/rename/setattr — and is the place where scratch
// buffering, path-level locks and editing policies live. Together
// they cover ~80% of what FUSE and WebDAV daemons need; the
// transport layer is a thin dispatcher.
//
// Schemas describing how artifacts map to filesystem paths live in
// subpackages (domain/vfsmeta is the standard one). They are
// pluggable through the PathResolver function passed to NewView.
//
// Specification: docs/3 §5 Projection API, docs/4 §13 Projection,
// docs/4 §14 FUSE Mount.
package projection

import (
	"scrinium.dev/projection/internal/fsops"
	"scrinium.dev/projection/internal/view"
)

// Projection bundles the read-side View with the optional read/write
// FSOps facade — the two ends of one projection over a store. They are
// always used together (a daemon reads trees through View and mutates
// through FSOps), and FSOps is constructed from a View, so pairing them
// in one value matches how callers consume them.
//
// FSOps is nil for a read-only projection; View is always present.
type Projection struct {
	// View is the read-side: the materialised trees (by-path, by-date,
	// …) over the store. Always non-nil in a built Projection.
	View *view.View

	// FSOps is the read/write filesystem facade over View. Nil when the
	// projection is read-only.
	FSOps *fsops.Ops
}

// Close releases the projection. FSOps holds no resources beyond the
// View it wraps, so closing the View is sufficient; the method exists
// so a Projection composes as an io.Closer alongside the store.
func (p *Projection) Close() error {
	if p == nil || p.View == nil {
		return nil
	}
	return p.View.Close()
}

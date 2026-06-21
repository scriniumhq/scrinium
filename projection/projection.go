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

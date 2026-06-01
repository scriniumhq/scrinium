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
// subpackages (projection/fsmeta is the standard one). They are
// pluggable through the PathResolver function passed to NewView.
//
// Specification: docs/3 §5 Projection API, docs/4 §13 Projection,
// docs/4 §14 FUSE Mount.
package projection

import (
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/event"
	"scrinium.dev/projection/node"
	"scrinium.dev/projection/source"
)

// --- View configuration ---

// PathFallback governs how artifacts without a resolver path are
// surfaced.
type PathFallback string

const (
	// FallbackOrphaned (default) — no by-path entry. Artifacts
	// remain reachable through by-artifact and the orphaned/
	// service tree.
	FallbackOrphaned PathFallback = "orphaned"

	// FallbackSynthetic — artifacts get a synthetic path derived
	// from namespace + session + short ArtifactID. Mixes real and
	// synthetic paths in by-path; for debugging on noisy stores.
	FallbackSynthetic PathFallback = "synthetic"
)

// ViewFilter restricts which manifests are admitted into the View
// during backfill. All non-zero conditions combine by AND.
type ViewFilter struct {
	Namespace string
	SessionID domain.SessionID
	Prefix    string
}

// ViewOption is the option type passed to NewView.
type ViewOption func(*viewOptions)

type viewOptions struct {
	resolver source.Resolver
	rootView node.RootView
	fallback PathFallback
	filter   ViewFilter
	bus      event.EventBus

	// extSource is an optional bulk source of manifest
	// metadata used by backfill to skip per-manifest
	// Source.Get round-trips. Set via WithExtSource or
	// WithFSIndex (the latter is a typed convenience for the
	// common projection/fsindex case).
	extSource source.Ext
}

// WithExtSource installs a bulk metadata source for
// backfill. When set, View.backfill consults the source instead
// of round-tripping Source.Get for each manifest. A miss
// (artifact not indexed by the source) falls back to Source.Get
// transparently — the option is a performance hint, not a
// correctness requirement.
func WithExtSource(ms source.Ext) ViewOption {
	return func(o *viewOptions) { o.extSource = ms }
}

// WithFSIndex is a typed convenience for the projection/fsindex
// case: pass the registered *fsindex.Extension and it doubles as
// a ExtSource. Equivalent to WithExtSource(fsidx).
//
// Implemented at the package level via an interface to avoid
// taking a hard dependency on projection/fsindex from
// projection — fsindex imports projection's fsmeta, so a back-
// edge would cycle.
func WithFSIndex(fsidx source.Ext) ViewOption {
	return WithExtSource(fsidx)
}

// WithPathResolver registers the path-extraction function. Without
// it the by-path tree contains only artifacts produced by the
// fallback (when FallbackSynthetic) or is empty.
func WithPathResolver(r source.Resolver) ViewOption {
	return func(o *viewOptions) { o.resolver = r }
}

// WithRootView selects the tree that occupies the View root. The
// default is RootByPath. The choice is informational for the View
// itself; transports (FUSE) react to it by hiding the same tree
// from the service directory.
func WithRootView(rv node.RootView) ViewOption {
	return func(o *viewOptions) { o.rootView = rv }
}

// WithFallback governs how artifacts without a resolver path are
// represented. Default: FallbackOrphaned.
func WithFallback(fb PathFallback) ViewOption {
	return func(o *viewOptions) { o.fallback = fb }
}

// WithFilter restricts the View to a subset of the source. Use for
// namespace-scoped or session-scoped views.
func WithFilter(f ViewFilter) ViewOption {
	return func(o *viewOptions) { o.filter = f }
}

// WithEventBus wires an event bus that receives EventViewRebuilt
// after backfill and EventPathCollision on each by-path
// collision. nil bus (the default when this option is not used)
// silently drops events.
func WithEventBus(bus event.EventBus) ViewOption {
	return func(o *viewOptions) { o.bus = bus }
}

// ViewStats holds the aggregate counters of a View. Populated
// during backfill and updated by Add/Remove/Move calls.
type ViewStats struct {
	TotalNodes     int64
	TotalBytes     int64
	SessionCount   int64
	NamespaceCount int64
	OrphanedCount  int64
	CollisionCount int64
	ByStore        map[string]int64
	TransitCount   int64
}

// --- Events ---

const (
	// EventPathCollision is emitted when two artifacts compete for
	// the same by-path entry; the loser stays accessible through
	// by-artifact.
	EventPathCollision = "projection.path_collision"

	// EventViewRebuilt is emitted after a successful backfill.
	EventViewRebuilt = "projection.view_rebuilt"
)

// PathCollisionPayload carries the resolution data of a path
// collision. Winner is the artifact now holding the path; Loser
// is the artifact that lost it (still reachable through
// by-artifact).
type PathCollisionPayload struct {
	Path   string
	Winner domain.ArtifactID
	Loser  domain.ArtifactID
}

// ViewRebuiltPayload carries timing and counts of a backfill
// completion. NodeCount is the total number of nodes across every
// tree (one file artifact may appear under several trees).
type ViewRebuiltPayload struct {
	Duration  time.Duration
	NodeCount int64
}

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
	View *View

	// FSOps is the read/write filesystem facade over View. Nil when the
	// projection is read-only.
	FSOps *FSOps
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

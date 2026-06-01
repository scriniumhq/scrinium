package view

import (
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/event"
	"scrinium.dev/projection/internal/source"
)

// Fallback governs how artifacts without a resolver path are
// surfaced.
type Fallback string

const (
	// FallbackOrphaned (default) — no by-path entry. Artifacts
	// remain reachable through by-artifact and the orphaned/
	// service tree.
	FallbackOrphaned Fallback = "orphaned"

	// FallbackSynthetic — artifacts get a synthetic path derived
	// from namespace + session + short ArtifactID. Mixes real and
	// synthetic paths in by-path; for debugging on noisy stores.
	FallbackSynthetic Fallback = "synthetic"
)

// Filter restricts which manifests are admitted into the View
// during backfill. All non-zero conditions combine by AND.
type Filter struct {
	Namespace string
	SessionID domain.SessionID
	Prefix    string
}

// Option is the option type passed to New.
type Option func(*viewOptions)

type viewOptions struct {
	resolver source.Resolver
	rootView RootView
	fallback Fallback
	filter   Filter
	bus      event.EventBus

	// extSource is an optional bulk source of manifest
	// metadata used by backfill to skip per-manifest
	// Source.Get round-trips. Set via WithExtSource or
	// WithFSIndex (the latter is a typed convenience for the
	// common engine/index/fsindex case).
	extSource source.Ext
}

// WithExtSource installs a bulk metadata source for
// backfill. When set, View.backfill consults the source instead
// of round-tripping Source.Get for each manifest. A miss
// (artifact not indexed by the source) falls back to Source.Get
// transparently — the option is a performance hint, not a
// correctness requirement.
func WithExtSource(ms source.Ext) Option {
	return func(o *viewOptions) { o.extSource = ms }
}

// WithFSIndex is a typed convenience for the engine/index/fsindex
// case: pass the registered *fsindex.Extension and it doubles as
// a ExtSource. Equivalent to WithExtSource(fsidx).
//
// Implemented at the package level via an interface to avoid
// taking a hard dependency on engine/index/fsindex from
// projection — fsindex imports projection's fsmeta, so a back-
// edge would cycle.
func WithFSIndex(fsidx source.Ext) Option {
	return WithExtSource(fsidx)
}

// WithPathResolver registers the path-extraction function. Without
// it the by-path tree contains only artifacts produced by the
// fallback (when FallbackSynthetic) or is empty.
func WithPathResolver(r source.Resolver) Option {
	return func(o *viewOptions) { o.resolver = r }
}

// WithRootView selects the tree that occupies the View root. The
// default is RootByPath. The choice is informational for the View
// itself; transports (FUSE) react to it by hiding the same tree
// from the service directory.
func WithRootView(rv RootView) Option {
	return func(o *viewOptions) { o.rootView = rv }
}

// WithFallback governs how artifacts without a resolver path are
// represented. Default: FallbackOrphaned.
func WithFallback(fb Fallback) Option {
	return func(o *viewOptions) { o.fallback = fb }
}

// WithFilter restricts the View to a subset of the source. Use for
// namespace-scoped or session-scoped views.
func WithFilter(f Filter) Option {
	return func(o *viewOptions) { o.filter = f }
}

// WithEventBus wires an event bus that receives EventViewRebuilt
// after backfill and EventPathCollision on each by-path
// collision. nil bus (the default when this option is not used)
// silently drops events.
func WithEventBus(bus event.EventBus) Option {
	return func(o *viewOptions) { o.bus = bus }
}

// Stats holds the aggregate counters of a View. Populated
// during backfill and updated by Add/Remove/Move calls.
type Stats struct {
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

// RebuiltPayload carries timing and counts of a backfill
// completion. NodeCount is the total number of nodes across every
// tree (one file artifact may appear under several trees).
type RebuiltPayload struct {
	Duration  time.Duration
	NodeCount int64
}

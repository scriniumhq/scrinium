package view

import (
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
	rootView RootView
	fallback Fallback
	filter   Filter
	bus      event.EventBus

	// metadataSource is an optional bulk source of manifest
	// metadata used by backfill to skip per-manifest
	// Source.Get round-trips. Set via WithMetadataSource or
	// WithFSPathIndex (the latter is a typed convenience for the
	// common engine/index/fspathindex case).
	metadataSource source.Metadata

	// provided are the extension-contributed view definitions that
	// make up the View's non-intrinsic trees (ADR-98). Set via
	// WithProvidedViews — by-path (fspath) and by-namespace (the
	// namespace extension) arrive here like any other. The View builds
	// one tree per provided root with no knowledge of its domain.
	provided []ProvidedView
}

// ProvidedView is an extension-contributed view definition (ADR-98). The
// projection appends each active extension's provided views to its
// intrinsic core set, building one tree per provided root. The projection
// attaches no meaning to the root or to what Path computes — this is the
// generic "by-ext" rail by which fspath contributes by-path, the namespace
// extension contributes by-namespace, and so on.
type ProvidedView struct {
	// Root is the tree identifier (e.g. "by-path"). It is the key under
	// which the tree is addressed via GetIn/ListIn and surfaced by
	// transports.
	Root RootView
	// Path maps a manifest to its placement in this tree. ok=false means
	// the manifest is absent from the tree (routed to the orphan tree when
	// Orphans is set).
	Path func(domain.Manifest) (string, bool)
	// Collide marks a tree whose path keys are not artifact-unique, so
	// inserts run collision arbitration (freshest CreatedAt wins, tie →
	// larger ArtifactID). by-path sets this; id-shaped views do not.
	Collide bool
	// Orphans routes a Path()=!ok manifest to the orphan tree (by-path).
	Orphans bool
	// CountKey, when set, supplies the distinct-cardinality key the View
	// uses to maintain this view's Stats counter.
	CountKey func(domain.Manifest) (string, bool)
}

// WithProvidedViews appends extension-contributed view definitions to the
// View's intrinsic set. This is the generic rail (ADR-98) by which the
// assembler forwards every active extension's views; the View treats them
// uniformly alongside its core trees.
func WithProvidedViews(pvs ...ProvidedView) Option {
	return func(o *viewOptions) { o.provided = append(o.provided, pvs...) }
}

// WithMetadataSource installs a bulk metadata source for
// backfill. When set, View.backfill consults the source instead
// of round-tripping Source.Get for each manifest. A miss
// (artifact not indexed by the source) falls back to Source.Get
// transparently — the option is a performance hint, not a
// correctness requirement.
func WithMetadataSource(ms source.Metadata) Option {
	return func(o *viewOptions) { o.metadataSource = ms }
}

// WithFSPathIndex is a typed convenience for the engine/index/fsindex
// case: pass the registered *fspath.CustomIndex and it doubles as
// a MetadataSource. Equivalent to WithMetadataSource(fsidx).
//
// Implemented at the package level via an interface to avoid
// taking a hard dependency on engine/index/fspathindex from
// projection — fspathindex imports projection's vfsmeta, so a back-
// edge would cycle.
func WithFSPathIndex(fsidx source.Metadata) Option {
	return WithMetadataSource(fsidx)
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

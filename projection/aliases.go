package projection

import "scrinium.dev/projection/internal/view"

// Re-exported read-model types. External code (daemons, CLI flag
// parsers, the stats renderer) depends on these projection-level
// names; the view package that defines them is a projection internal
// and is never named outside the projection tree.
type (
	// RootView selects which materialised tree backs a lookup
	// (by-path, by-date, by-session, by-namespace, by-artifact,
	// orphaned).
	RootView = view.RootView

	// Stats is a snapshot of projection counters.
	Stats = view.Stats

	// SearchResult is one hit from Reader.Search.
	SearchResult = view.SearchResult

	// RelatedArtifact is one entry from Reader.RelatedByBlobRef.
	RelatedArtifact = view.RelatedArtifact

	// Locations bundles every tree-placement of one artifact.
	Locations = view.Locations
)

// RootView values, re-exported so flag parsers and configs can name
// the enum without reaching into the view package.
const (
	RootByPath      = view.RootByPath
	RootBySession   = view.RootBySession
	RootByNamespace = view.RootByNamespace
	RootByDate      = view.RootByDate
	RootByArtifact  = view.RootByArtifact
	RootByOrphaned  = view.RootByOrphaned
)

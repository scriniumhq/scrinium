package projection

import (
	"scrinium.dev/projection/internal/source"
	"scrinium.dev/projection/internal/view"
)

// Re-exported read-model types. External code (daemons, CLI flag
// parsers, the stats renderer) depends on these projection-level
// names; the view package that defines them is a projection internal
// and is never named outside the projection tree.
type (
	// RootView selects which materialised tree backs a lookup. The
	// intrinsic trees are by-date, by-session, by-artifact, and
	// orphaned; extensions contribute further roots at runtime.
	RootView = view.RootView

	// Stats is a snapshot of projection counters.
	Stats = view.Stats

	// SearchResult is one hit from Reader.Search.
	SearchResult = view.SearchResult

	// RelatedArtifact is one entry from Reader.RelatedByBlobRef.
	RelatedArtifact = view.RelatedArtifact

	// Locations bundles every tree-placement of one artifact.
	Locations = view.Locations

	// TokenSource is the pull half of the synchronization seam (ADR-107): the
	// backend's current change-sequence, adapted from the index's SyncSource
	// capability by the composition root. A nil SyncSource in Config gives the
	// View snapshot semantics.
	TokenSource = source.TokenSource

	// Waiter is the optional push half (ADR-107), adapted from the index's
	// SyncWaiter. With it the View can refresh eagerly instead of polling.
	Waiter = source.Waiter
)

// RootView values, re-exported so flag parsers and configs can name the
// intrinsic enum without reaching into the view package. Extension-
// contributed roots (by-path, by-namespace, …) are not re-exported — the
// projection names none of them; they are discovered at runtime.
const (
	RootBySession  = view.RootBySession
	RootByDate     = view.RootByDate
	RootByArtifact = view.RootByArtifact
	RootByOrphaned = view.RootByOrphaned
)

package view

import (
	"time"

	"scrinium.dev/domain"
)

// queries.go — the public result types of the View's read methods,
// re-exported by the projection facade (projection/aliases.go). They are
// kept apart from the node/facet vocabulary (node.go) and the View's
// private runtime state (types.go) so the outward query surface reads on
// its own.

// Stats holds aggregated projection-wide storage and node metrics.
type Stats struct {
	TotalNodes   int64 `json:"totalNodes"`
	TotalBytes   int64 `json:"totalBytes"`
	SessionCount int64 `json:"sessionCount"`
	// ViewCounts holds the distinct-key cardinality of each counting
	// view, keyed by root. by-session keeps its own named counter
	// above (it is an intrinsic view); every other counting view —
	// including any extension-provided one — lands here, so the
	// projection names none of them.
	ViewCounts     map[RootView]int64 `json:"viewCounts"`
	OrphanedCount  int64              `json:"orphanedCount"`
	CollisionCount int64              `json:"collisionCount"`
	ByStore        map[string]int64   `json:"byStore"`
	TransitCount   int64              `json:"transitCount"`
}

// SearchResult represents a single item found during index lookups.
type SearchResult struct {
	ArtifactID  domain.ArtifactID `json:"artifactId"`
	Path        string            `json:"path"`
	SessionID   domain.SessionID  `json:"sessionId"`
	CreatedAt   time.Time         `json:"createdAt"`
	MIME        string            `json:"mime"`
	MatchReason string            `json:"matchReason"`
}

// RelatedArtifact contains reference data for linked artifacts within the tree.
type RelatedArtifact struct {
	ArtifactID domain.ArtifactID `json:"artifactId"`
	Path       string            `json:"path"`
	SessionID  domain.SessionID  `json:"sessionId"`
	CreatedAt  time.Time         `json:"createdAt"`
}

// Locations maps each root view a manifest appears in to its path
// within that view. Keys are whatever roots are active — intrinsic
// (by-artifact/by-date/by-session/by-orphaned) plus any extension-
// provided — so the projection names none of them.
type Locations struct {
	Paths map[RootView]string `json:"paths"`
}

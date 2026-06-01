package projection

import (
	"time"

	"scrinium.dev/domain"
)

// RootView defines the grouping strategy for the projection tree hierarchy.
type RootView string

const (
	RootByPath      RootView = "by-path"
	RootBySession   RootView = "by-session"
	RootByNamespace RootView = "by-namespace"
	RootByDate      RootView = "by-date"
	RootByArtifact  RootView = "by-artifact"
	RootByOrphaned  RootView = "by-orphaned"
)

// AllRootViews contains a slice of all supported RootView types for iteration.
var AllRootViews = []RootView{
	RootByPath,
	RootBySession,
	RootByNamespace,
	RootByDate,
	RootByArtifact,
	RootByOrphaned,
}

// IsValid validates whether the RootView value belongs to the allowed enum set.
func (r RootView) IsValid() bool {
	for _, v := range AllRootViews {
		if r == v {
			return true
		}
	}
	return false
}

// Stats holds aggregated projection-wide storage and node metrics.
type Stats struct {
	TotalNodes     int64            `json:"totalNodes"`
	TotalBytes     int64            `json:"totalBytes"`
	SessionCount   int64            `json:"sessionCount"`
	NamespaceCount int64            `json:"namespaceCount"`
	OrphanedCount  int64            `json:"orphanedCount"`
	CollisionCount int64            `json:"collisionCount"`
	ByStore        map[string]int64 `json:"byStore"`
	TransitCount   int64            `json:"transitCount"`
}

// SearchResult represents a single item found during index lookups.
type SearchResult struct {
	ArtifactID  domain.ArtifactID `json:"artifactId"`
	Path        string            `json:"path"`
	Namespace   string            `json:"namespace"`
	SessionID   domain.SessionID  `json:"sessionId"`
	CreatedAt   time.Time         `json:"createdAt"`
	MIME        string            `json:"mime"`
	MatchReason string            `json:"matchReason"`
}

// RelatedArtifact contains reference data for linked artifacts within the tree.
type RelatedArtifact struct {
	ArtifactID domain.ArtifactID `json:"artifactId"`
	Path       string            `json:"path"`
	Namespace  string            `json:"namespace"`
	SessionID  domain.SessionID  `json:"sessionId"`
	CreatedAt  time.Time         `json:"createdAt"`
}

// Locations specifies target directory mappings for different root structures.
type Locations struct {
	ByArtifact  string `json:"byArtifact"`
	BySession   string `json:"bySession"`
	ByNamespace string `json:"byNamespace"`
	ByDate      string `json:"byDate"`
	ByPath      string `json:"byPath"`
	ByOrphaned  string `json:"byOrphaned"`
}

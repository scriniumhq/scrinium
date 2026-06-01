package view

import (
	"sync"
	"sync/atomic"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/event"
	"scrinium.dev/projection/internal/source"
)

type View struct {
	// Public, read-only after New returns.
	Source    source.Kind
	CreatedAt time.Time
	Stats     Stats

	// Internal state, guarded by mu.
	mu sync.RWMutex

	// Trees. Each map keys by full path (no leading slash, ""
	// means tree root). Values are the canonical viewNode for
	// that path.
	byPath      map[string]*viewNode
	bySession   map[string]*viewNode
	byNamespace map[string]*viewNode
	byDate      map[string]*viewNode
	byArtifact  map[string]*viewNode
	// byOrphaned holds artifacts the path resolver couldn't
	// place — typically system manifests or artifacts written
	// without an fsmeta payload. Same id-shaped layout as
	// byArtifact (aa/bb/<id>) so the same lookup helpers work.
	// Unlike byArtifact (which contains every artifact), this
	// one only contains the ones missing from byPath.
	byOrphaned map[string]*viewNode

	// Per-artifact tracking. Used by Remove/Move to fan out a
	// deletion or move across every tree without re-deriving the
	// paths.
	artifacts map[domain.ArtifactID]*artifactRecord

	// Path-collision bookkeeping for by-path. pathOwner records the
	// ArtifactID currently holding each path; pathLosers stores
	// every other ArtifactID claiming the same path, sorted by
	// CreatedAt descending so the freshest loser is at index 0.
	// On Remove of the current owner we promote pathLosers[path][0]
	// to owner; on a new Add against an existing owner we compare
	// CreatedAt to decide whether the newcomer becomes owner or
	// joins the losers list.
	pathOwner  map[string]domain.ArtifactID
	pathLosers map[string][]loserEntry

	// For Stats: track unique sessions and namespaces seen.
	seenSessions   map[domain.SessionID]struct{}
	seenNamespaces map[string]struct{}

	src    source.Provider
	bus    event.EventBus // nil = events not published
	opts   viewOptions
	closed atomic.Bool
}

// viewNode is the internal node representation. The public Node
// is built from these fields when read.
type viewNode struct {
	fs       FilesystemFacet
	artifact *ArtifactFacet // nil for virtual directories
	children []string       // sorted last-segment names; nil for files
}

// artifactRecord is the cross-tree record of an artifact: the
// manifest plus the path under which it appears in every tree.
// Empty paths mean the artifact is absent from that tree (e.g.,
// no by-path entry when Resolver returned !ok and fallback is
// orphaned).
type artifactRecord struct {
	manifest        domain.Manifest
	pathByArtifact  string
	pathBySession   string
	pathByNamespace string
	pathByDate      string
	pathByPath      string // "" if artifact is orphaned
	pathByOrphaned  string // "" if artifact is in byPath
}

// loserEntry records a losing artifact in a path collision —
// the ArtifactID and its CreatedAt for re-election ordering.
type loserEntry struct {
	id        domain.ArtifactID
	createdAt time.Time
}
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

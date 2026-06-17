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

	// trees holds one tree per active RootView (the intrinsic core
	// set plus every extension-provided view, see defs). Each tree
	// maps a full path (no leading slash, "" is the tree root) to
	// the canonical viewNode. RootByOrphaned is always present as the
	// sink for artifacts an orphaning view (by-path) could not place.
	trees map[RootView]map[string]*viewNode

	// defs is the active view set: the intrinsic definitions the View
	// builds from core manifest fields, augmented by the views active
	// extensions provide (ADR-98). indexArtifact iterates it; the View
	// attaches no meaning to any individual root.
	defs []viewDef

	// Per-artifact tracking. Used by Remove/Move to fan out a
	// deletion or move across every tree without re-deriving the
	// paths. paths maps each RootView the artifact appears in to its
	// path there (including RootByOrphaned when orphaned).
	artifacts map[domain.ArtifactID]*artifactRecord

	// Path-collision bookkeeping, keyed by RootView then path, for
	// the collidable views (those whose path keys are not artifact-
	// unique — by-path). pathOwner records the ArtifactID currently
	// holding each path; pathLosers stores every other ArtifactID
	// claiming it, sorted by CreatedAt descending so the freshest
	// loser is at index 0. On Remove of the owner we promote
	// pathLosers[root][path][0]; on a new insert against an existing
	// owner we compare CreatedAt to decide winner vs loser. collide
	// marks which roots run this arbitration.
	pathOwner  map[RootView]map[string]domain.ArtifactID
	pathLosers map[RootView]map[string][]loserEntry
	collide    map[RootView]bool

	// seenKeys tracks, per counting view, the distinct cardinality
	// keys observed (session ids, nsids), so Stats counters stay
	// accurate without the View knowing the concept. Monotonic:
	// entries are never removed (matching the existing Stats
	// semantics for SessionCount/NamespaceCount).
	seenKeys map[RootView]map[string]struct{}

	src    source.Provider
	bus    event.EventBus // nil = events not published
	opts   viewOptions
	closed atomic.Bool
}

// viewDef describes one logical tree. Intrinsic defs (by-artifact,
// by-date, by-session) are built by the View from core manifest fields;
// extension-provided defs (by-path, by-namespace, …) augment the set via
// ProvidedView. The View is agnostic to what each def means.
type viewDef struct {
	root RootView
	// path maps a manifest to its placement in this tree. ok=false (or a
	// nil path) means the manifest has no place here: it is routed to the
	// orphan tree when orphans is set, otherwise skipped.
	path func(domain.Manifest) (string, bool)
	// collide marks a tree whose path keys are NOT artifact-unique (the
	// by-path logical namespace): inserts run collision arbitration.
	collide bool
	// orphans routes a path()=!ok manifest to the orphan tree (or a
	// synthetic path under FallbackSynthetic); other defs skip a miss.
	orphans bool
	// countKey, when non-nil, supplies this view's distinct-cardinality
	// key so the View maintains the matching Stats counter.
	countKey func(domain.Manifest) (string, bool)
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
// paths maps each RootView the artifact occupies to its path there;
// a root absent from the map means the artifact is not in that tree
// (e.g. no by-path entry when the resolver missed and it was
// orphaned — then RootByOrphaned is present instead).
type artifactRecord struct {
	manifest domain.Manifest
	paths    map[RootView]string
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

// Locations maps each root view a manifest appears in to its path
// within that view. Keys are whatever roots are active — intrinsic
// (by-artifact/by-date/by-session/by-orphaned) plus any extension-
// provided — so the projection names none of them.
type Locations struct {
	Paths map[RootView]string `json:"paths"`
}

// Package view is the data vocabulary of the projection layer: the
// shape of a projected entry (Node + its facets), the RootView
// selector that names a logical tree, and the read-query result types
// (Stats, SearchResult, RelatedArtifact, Locations). It is a leaf — it
// depends only on the domain package and the standard library, so every
// other projection package (routing, view, fsops, vfs) and external
// consumers speak this vocabulary without dragging in the read model
// or the filesystem operations.
package view

import (
	"encoding/json"
	"iter"
	"time"

	"scrinium.dev/domain"
)

// FilesystemFacet is the path/name/size/time view of a node. Always
// populated, including for virtual directories synthesised from
// grouping.
//
// POSIX attributes (mode, uid, gid) are NOT in this facet: they
// belong to the filesystem schema (vfsmeta.FileSystem) and are
// materialised by fsops at the transport boundary (FUSE/WebDAV).
type FilesystemFacet struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// ArtifactFacet carries the CAS metadata of a concrete artifact.
// Populated for file nodes; nil for virtual directories.
type ArtifactFacet struct {
	ArtifactID  domain.ArtifactID
	ContentHash domain.ContentHash
	BlobRef     domain.BlobRef
	SessionID   domain.SessionID
	CreatedAt   time.Time

	// Ext carries the engine-custom index metadata block (vfsmeta and
	// friends). Per ADR-54 the Usr block is intentionally not
	// surfaced at facet level.
	Ext json.RawMessage
}

// StorageFacet carries placement data within a multistore stack.
// Populated only when the backing source is a multistore.
type StorageFacet struct {
	StoreID  domain.StoreID
	RefCount int
}

// Node is one entry in a view. FS is always populated; Artifact for
// files; Storage only on a multistore source.
type Node struct {
	FS       FilesystemFacet
	Artifact *ArtifactFacet
	Storage  *StorageFacet
}

// Seq is a sequence of nodes with an optional error per position
// (the standard iter.Seq2 pattern for fallible streams).
type Seq = iter.Seq2[Node, error]

// --- Read-query vocabulary ---
//
// These are the public result types of the View's read methods,
// re-exported by the projection facade (projection/aliases.go). They
// live here with Node and the facets so the projection's outward
// vocabulary sits in one leaf file, apart from the View's private
// runtime state (types.go).

type RootView string

const (
	RootBySession  RootView = "by-session"
	RootByDate     RootView = "by-date"
	RootByArtifact RootView = "by-artifact"
	RootByOrphaned RootView = "by-orphaned"
)

// AllRootViews lists the intrinsic root views the projection owns. Roots
// contributed by extensions (via the provided-view rail) are NOT listed
// here — the projection names none of them; enumerate them at runtime via
// View.ProvidedRoots.
var AllRootViews = []RootView{
	RootBySession,
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

// Package node is the data vocabulary of the projection layer: the
// shape of a projected entry (Node + its facets) and the RootView
// selector that names a logical tree. It is a leaf — it depends only
// on the domain package and the standard library, so every other
// projection package (routing, view, fsops, vfs) and external
// consumers speak this vocabulary without dragging in the read model
// or the filesystem operations.
package node

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
// belong to the filesystem schema (fsmeta.FileSystem) and are
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
	Namespace   string
	SessionID   domain.SessionID
	CreatedAt   time.Time
	Type        domain.ManifestType

	// Ext carries the engine-extension metadata block (fsmeta and
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

// RootView selects which logical tree appears at the root of a view.
type RootView string

const (
	RootByPath      RootView = "by-path" // default
	RootBySession   RootView = "by-session"
	RootByNamespace RootView = "by-namespace"
	RootByDate      RootView = "by-date"
	RootByArtifact  RootView = "by-artifact"
	RootByOrphaned  RootView = "by-orphaned"
)

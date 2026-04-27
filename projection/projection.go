package projection

import (
	"context"
	"errors"
	"io"
	"iter"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
)

// NodeSeq is a sequence of nodes with an optional error attached
// to each position.
type NodeSeq = iter.Seq2[Node, error]

// SourceKind is the type of the Projection's data source. It
// determines whether StorageFacet is present in a Node.
type SourceKind uint8

const (
	// SourceKindStore — a single core.DataStore. StorageFacet is
	// always nil.
	SourceKindStore SourceKind = iota

	// SourceKindCurator — a Curator with access to MultistoreIndex.
	// StorageFacet is populated when needed.
	SourceKindCurator
)

// ProjectionSource is the minimal contract for an artifact source
// supplying a Projection. It does not depend on curator: it is
// satisfied by both core.DataStore and curator.Curator. Extended
// abilities (StorageFacet) are detected via a type assertion to
// *curator.Curator on the Projection side — a deliberate decision
// that avoids leaking curator dependencies down the DAG.
type ProjectionSource interface {
	Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error
	Get(ctx context.Context, id domain.ArtifactID, opts core.GetOptions) (core.ReadHandle, error)
}

// PathResolver extracts the virtual path from a manifest. It
// returns a path in the "a/b/c.txt" style or an empty string if
// no path is defined (the artifact will appear only under
// by-artifact/).
type PathResolver func(m domain.Manifest) string

// --- Node and facets ---

// FilesystemFacet is the part of every Node that is always filled.
type FilesystemFacet struct {
	Path     string
	IsDir    bool
	Size     int64
	ModTime  time.Time
	Children []string
}

// ArtifactFacet holds the data of a concrete artifact. Filled for
// file nodes; nil for directories.
type ArtifactFacet struct {
	ArtifactID domain.ArtifactID
	Manifest   domain.Manifest
}

// StorageFacet holds placement data within the Curator stack.
// Filled only when SourceKind == Curator. nil when SourceKind ==
// Store.
type StorageFacet struct {
	StoreID   domain.StoreID
	Workspace domain.Workspace
	IsTransit bool
	RefCount  int
}

// Node is a node in the View tree. FilesystemFacet is always
// populated; ArtifactFacet is populated for files; StorageFacet
// only for a Curator source.
type Node struct {
	Filesystem FilesystemFacet
	Artifact   *ArtifactFacet
	Storage    *StorageFacet
}

// --- View ---

// ViewMode is the way the View's contents are computed.
type ViewMode string

const (
	// ViewModeSnapshot — an in-memory snapshot at CreateView time.
	// Subsequent changes in the source are not reflected. Cheap
	// and deterministic.
	ViewModeSnapshot ViewMode = "Snapshot"

	// ViewModeLive — reactive updates driven by events. Backlog:
	// implementation lands in v1.x.
	ViewModeLive ViewMode = "Live"
)

// ViewOption is an option for the View constructor.
type ViewOption func(*viewOptions)

type viewOptions struct {
	mode      ViewMode
	resolver  PathResolver
	namespace string
	filter    ViewFilter
}

// WithViewMode switches the View computation mode.
func WithViewMode(mode ViewMode) ViewOption {
	return func(o *viewOptions) { o.mode = mode }
}

// WithPathResolver provides the path-extraction function. Without
// it the by-path/ tree stays empty; artifacts remain reachable
// only through by-artifact/.
func WithPathResolver(fn PathResolver) ViewOption {
	return func(o *viewOptions) { o.resolver = fn }
}

// WithNamespace restricts the View to a single namespace. The
// default is every user namespace (system.* is always excluded).
func WithNamespace(ns string) ViewOption {
	return func(o *viewOptions) { o.namespace = ns }
}

// WithFilter provides a manifest-filter predicate.
func WithFilter(filter ViewFilter) ViewOption {
	return func(o *viewOptions) { o.filter = filter }
}

// ViewFilter is the manifest-inclusion predicate for a View.
// true means include.
type ViewFilter func(m domain.Manifest) bool

// View is an open representation. Every method is safe for
// concurrent reads. After Close every call returns ErrViewClosed.
type View interface {
	// Get returns the Node at a virtual path.
	Get(path string) (Node, error)

	// List returns the immediate children of a directory.
	List(path string) NodeSeq

	// Walk recursively iterates the subtree under prefix.
	Walk(prefix string) NodeSeq

	// Open opens the artifact data stream at the virtual path.
	Open(ctx context.Context, path string) (io.ReadCloser, error)

	// Stats returns aggregated metrics of the View.
	Stats() ViewStats

	// Close releases the View's resources.
	Close() error
}

// ViewStats are the aggregates of the built View.
type ViewStats struct {
	NodesTotal       int64
	FilesTotal       int64
	DirectoriesTotal int64
	BytesTotal       int64
	CollisionsCount  int64
	BuildDuration    time.Duration
}

// --- Mounts (FUSE, WebDAV) ---

// MountFUSEConfig configures a FUSE mount.
type MountFUSEConfig struct {
	MountPoint string
	ReadOnly   bool
}

// MountWebDAVConfig configures the WebDAV server.
type MountWebDAVConfig struct {
	Addr     string
	ReadOnly bool
}

// --- Projection facade ---

// Projection is the main entry point. It creates Views, mounts
// them, and supports background operations.
type Projection interface {
	// CreateView creates a View on top of the source.
	CreateView(ctx context.Context, opts ...ViewOption) (View, error)

	// MountFUSE mounts a View through FUSE. Available only with
	// the `fuse` build tag. Without it returns ErrFUSENotSupported.
	MountFUSE(ctx context.Context, view View, cfg MountFUSEConfig) (Mount, error)

	// MountWebDAV runs a WebDAV server on top of a View. Available
	// with the `webdav` build tag. Without it returns
	// ErrWebDAVNotSupported.
	MountWebDAV(ctx context.Context, view View, cfg MountWebDAVConfig) (Mount, error)

	// Close stops every active mount and releases resources.
	Close(ctx context.Context) error
}

// Mount is an active mount point. Returned by
// MountFUSE/MountWebDAV.
type Mount interface {
	Unmount(ctx context.Context) error
}

// NewProjection creates a Projection on top of the source.
// Implementation lands in M6.1.
func NewProjection(source ProjectionSource) (Projection, error) {
	return nil, errors.New("projection.NewProjection: not implemented")
}

// --- Events ---

const (
	EventPathCollision = "projection.path_collision"
	EventViewRebuilt   = "projection.view_rebuilt"
)

// PathCollisionPayload is the payload of EventPathCollision.
// Emitted on a path-collision resolution: the winner stays in
// by-path/, the loser remains reachable only through by-artifact/.
type PathCollisionPayload struct {
	Path       string
	WinnerID   domain.ArtifactID
	LoserID    domain.ArtifactID
	Resolution string
}

// ViewRebuiltPayload is the payload of EventViewRebuilt. Emitted
// on a full View rebuild (Live mode, M6+).
type ViewRebuiltPayload struct {
	ViewID    string
	Trigger   string
	NodeCount int64
	Duration  time.Duration
}

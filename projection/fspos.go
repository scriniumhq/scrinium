package projection

import (
	"context"
	"fmt"
	"io"
	"iter"
	"sync"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// FSOps is the filesystem-shaped operations layer over a View.
// It serves transports (FUSE, WebDAV) by translating
// path-keyed POSIX-like calls into View lookups (read side) and,
// in stage 4b, into Store mutations (write side).
//
// The two reasons FSOps exists rather than the transport calling
// View directly:
//
//  1. Editing policy, scratch buffering, path-level locks live in
//     one place — FUSE and WebDAV inherit them by construction.
//  2. The transport works in terms of the configured root tree
//     (RootView). FSOps hides that routing from the caller.
//
// Stage 4a: read-side (Stat, Listdir, Open). Mutations land in 4b.
type FSOps struct {
	view *View

	store StoreClient // unused in 4a; required by 4b's mutation paths

	scratchDir   string
	scratchQuota int64

	defaultMode uint32
	defaultUID  uint32
	defaultGID  uint32

	editing      EditingPolicy
	mountSession string
	namespace    string
	readOnly     bool

	// Path lock manager. Stage 4a does not actually take any
	// locks — reads through View are concurrency-safe on their
	// own — but we initialise the manager here so the field is
	// stable across stages.
	pathLocks *pathLockManager
}

// StoreClient is the write-side surface FSOps depends on. Defined
// here rather than reusing core.Store so that:
//
//   - the dependency is minimal — FSOps does not need namespace
//     enumeration, lifecycle, crypto admin, or any of core's
//     other surface;
//   - tests can supply a fake without implementing every method
//     of core.Store.
//
// core.Store satisfies this interface naturally (subset typing
// in Go).
type StoreClient interface {
	Put(ctx context.Context, a domain.Artifact, opts domain.PutOptions) (domain.ArtifactID, error)
	Delete(ctx context.Context, id domain.ArtifactID) error
	Get(ctx context.Context, id domain.ArtifactID, opts domain.GetOptions) (core.ReadHandle, error)
}

// FileInfo is the POSIX-shaped descriptor that Stat/Listdir
// returns. Built from FilesystemFacet plus FSOps defaults.
type FileInfo struct {
	Name    string
	Path    string
	Size    int64
	Mode    uint32
	UID     uint32
	GID     uint32
	ModTime time.Time
	IsDir   bool
}

// FileInfoSeq is a stream of FileInfo with optional error per
// position; mirrors NodeSeq.
type FileInfoSeq = iter.Seq2[FileInfo, error]

// File is the handle returned by Open/Create. It bundles random
// I/O, sync, in-place truncate, and Close — together they cover
// what FUSE write paths need.
//
// Stage 4a: only read-only handles are produced (via Open with
// OpenReadOnly). Write methods (WriteAt, Truncate, Sync) on a
// read-only handle return ErrEditingDisabled.
type File interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
	Sync() error
	Truncate(size int64) error
}

// OpenMode is the access mode for Open. Bit-flags: combine with
// OR (e.g. OpenReadWrite | OpenAppend).
type OpenMode int

const (
	OpenReadOnly  OpenMode = 0
	OpenWriteOnly OpenMode = 1
	OpenReadWrite OpenMode = 2
	OpenAppend    OpenMode = 4
)

// Attrs is the set of attribute updates passed to Setattr. nil
// fields mean "leave unchanged".
type Attrs struct {
	Mode    *uint32
	UID     *uint32
	GID     *uint32
	ModTime *time.Time
}

// EditingPolicy is the per-operation switchboard for the editing
// surface (rename, setattr, truncate, append). Each bit is
// independent; helpers EditingOff / EditingOn are sugar.
type EditingPolicy struct {
	AllowRename   bool
	AllowSetattr  bool
	AllowTruncate bool
	AllowAppend   bool
}

// EditingOff is the conservative default: no editing of existing
// artifacts. Create and Unlink still work.
func EditingOff() EditingPolicy { return EditingPolicy{} }

// EditingOn enables every editing capability. Use only when
// callers understand the CAS implications (every mutation
// produces a new artifact).
func EditingOn() EditingPolicy {
	return EditingPolicy{
		AllowRename:   true,
		AllowSetattr:  true,
		AllowTruncate: true,
		AllowAppend:   true,
	}
}

// FSOpsOption configures NewFSOps.
type FSOpsOption func(*fsOpsOptions)

type fsOpsOptions struct {
	store        StoreClient
	scratchDir   string
	scratchQuota int64
	defaultMode  uint32
	defaultUID   uint32
	defaultGID   uint32
	editing      EditingPolicy
	mountSession string
	namespace    string
	readOnly     bool
}

func WithStore(s StoreClient) FSOpsOption {
	return func(o *fsOpsOptions) { o.store = s }
}

// WithScratchDir sets the directory for scratch files. Must
// exist and be writable. Scratch is created lazily on the first
// Create/Open(write).
func WithScratchDir(path string) FSOpsOption {
	return func(o *fsOpsOptions) { o.scratchDir = path }
}

// WithScratchQuota caps the total bytes held by active scratch
// files. 0 means unlimited.
func WithScratchQuota(bytes int64) FSOpsOption {
	return func(o *fsOpsOptions) { o.scratchQuota = bytes }
}

// WithDefaultMode is the POSIX mode applied to artifacts whose
// fsmeta.Mode is zero. Default 0644.
func WithDefaultMode(mode uint32) FSOpsOption {
	return func(o *fsOpsOptions) { o.defaultMode = mode }
}

// WithDefaultUID applies to fsmeta.UID == 0.
func WithDefaultUID(uid uint32) FSOpsOption {
	return func(o *fsOpsOptions) { o.defaultUID = uid }
}

// WithDefaultGID applies to fsmeta.GID == 0.
func WithDefaultGID(gid uint32) FSOpsOption {
	return func(o *fsOpsOptions) { o.defaultGID = gid }
}

// WithEditingPolicy gates editing operations. Default:
// EditingOff.
func WithEditingPolicy(p EditingPolicy) FSOpsOption {
	return func(o *fsOpsOptions) { o.editing = p }
}

// WithMountSession sets the SessionID stamped onto every Put
// performed through FSOps in this mount. Empty means "no
// session" — artifacts will not appear in by-session.
func WithMountSession(sid string) FSOpsOption {
	return func(o *fsOpsOptions) { o.mountSession = sid }
}

// WithNamespace sets the Namespace stamped onto every Put.
// Required when Create is called; Mkdir/Rmdir/Unlink/Rename
// do not depend on it (they operate on existing artifacts).
func WithNamespace(ns string) FSOpsOption {
	return func(o *fsOpsOptions) { o.namespace = ns }
}

// WithReadOnly forces every mutation to return ErrEditingDisabled
// regardless of EditingPolicy or the existence of a StoreClient.
func WithReadOnly() FSOpsOption {
	return func(o *fsOpsOptions) { o.readOnly = true }
}

// NewFSOps wraps a View with filesystem-shaped operations.
//
// The View must already exist (the caller is responsible for the
// View's lifecycle); NewFSOps does not call NewView itself
// because the View may be shared with other transports.
//
// Defaults:
//
//   - DefaultMode 0644, DefaultUID/GID 0
//   - EditingOff (no rename, setattr, truncate, append)
//   - ScratchQuota 0 (unlimited; the OS still imposes its own)
//
// Returns an error only if v is nil. Configuration sanity is
// otherwise the caller's responsibility (e.g. an invalid
// scratch dir surfaces only at the first Create call).
func NewFSOps(v *View, opts ...FSOpsOption) (*FSOps, error) {
	if v == nil {
		return nil, fmt.Errorf("projection.NewFSOps: view is nil")
	}
	o := fsOpsOptions{
		defaultMode: 0o644,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return &FSOps{
		view:         v,
		store:        o.store,
		scratchDir:   o.scratchDir,
		scratchQuota: o.scratchQuota,
		defaultMode:  o.defaultMode,
		defaultUID:   o.defaultUID,
		defaultGID:   o.defaultGID,
		editing:      o.editing,
		mountSession: o.mountSession,
		namespace:    o.namespace,
		readOnly:     o.readOnly,
		pathLocks:    newPathLockManager(),
	}, nil
}

// --- Read side ---

// Stat returns the FileInfo for a virtual path interpreted in the
// configured root tree.
func (o *FSOps) Stat(path string) (FileInfo, error) {
	n, err := o.lookupInRoot(path)
	if err != nil {
		return FileInfo{}, err
	}
	return o.fileInfoFromNode(n), nil
}

// Listdir streams the immediate children of path. Returns
// ErrIsADirectory? No — see ListdirOnFile semantics. On a file,
// the caller gets ErrNotADirectory; on a missing path,
// ErrPathNotFound.
func (o *FSOps) Listdir(path string) FileInfoSeq {
	return func(yield func(FileInfo, error) bool) {
		seq := o.listInRoot(path)
		for n, err := range seq {
			if err != nil {
				yield(FileInfo{}, err)
				return
			}
			if !yield(o.fileInfoFromNode(n), nil) {
				return
			}
		}
	}
}

// Open returns a File handle for reading. mode is honoured for
// stage-4a only insofar as OpenReadOnly is permitted; any other
// mode returns ErrEditingDisabled (write paths land in 4b).
func (o *FSOps) Open(ctx context.Context, path string, mode OpenMode) (File, error) {
	if mode != OpenReadOnly {
		// Stage 4a: write/append handles land in 4b.
		return nil, fmt.Errorf("%w: write-side Open", errs.ErrNotImplemented)
	}
	n, err := o.lookupInRoot(path)
	if err != nil {
		return nil, err
	}
	if n.FS.IsDir {
		return nil, fmt.Errorf("%w: %q", errs.ErrIsADirectory, path)
	}
	rh, err := o.openInRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	return &readOnlyFile{rh: rh}, nil
}

// --- Read-side helpers (router into View per RootView) ---

// lookupInRoot routes Get to the tree configured as the root.
func (o *FSOps) lookupInRoot(path string) (Node, error) {
	switch o.view.RootView() {
	case RootByPath:
		return o.view.GetByPath(path)
	case RootBySession:
		return o.view.GetBySession(path)
	case RootByNamespace:
		return o.view.GetByNamespace(path)
	case RootByDate:
		return o.view.GetByDate(path)
	case RootByArtifact:
		return o.view.GetByArtifact(path)
	default:
		// Unknown RootView — should not happen because the enum
		// is closed; treat as misconfiguration.
		return Node{}, fmt.Errorf("projection.FSOps: unknown RootView %q", o.view.RootView())
	}
}

func (o *FSOps) listInRoot(path string) NodeSeq {
	switch o.view.RootView() {
	case RootByPath:
		return o.view.ListByPath(path)
	case RootBySession:
		return o.view.ListBySession(path)
	case RootByNamespace:
		return o.view.ListByNamespace(path)
	case RootByDate:
		return o.view.ListByDate(path)
	case RootByArtifact:
		return o.view.ListByArtifact(path)
	default:
		return func(yield func(Node, error) bool) {
			yield(Node{}, fmt.Errorf("projection.FSOps: unknown RootView %q", o.view.RootView()))
		}
	}
}

func (o *FSOps) openInRoot(ctx context.Context, path string) (core.ReadHandle, error) {
	switch o.view.RootView() {
	case RootByPath:
		return o.view.OpenByPath(ctx, path, domain.GetOptions{})
	case RootBySession:
		return o.view.OpenBySession(ctx, path, domain.GetOptions{})
	case RootByNamespace:
		return o.view.OpenByNamespace(ctx, path, domain.GetOptions{})
	case RootByDate:
		return o.view.OpenByDate(ctx, path, domain.GetOptions{})
	case RootByArtifact:
		return o.view.OpenByArtifact(ctx, path, domain.GetOptions{})
	default:
		return nil, fmt.Errorf("projection.FSOps: unknown RootView %q", o.view.RootView())
	}
}

// fileInfoFromNode converts a Node into a FileInfo, applying
// FSOps defaults to fields the artifact left zero.
//
// For file nodes, FSOps reads fsmeta from the Manifest.Metadata
// directly — the View itself is schema-agnostic and does not
// surface fsmeta.Mode/UID/GID/ModTime through FilesystemFacet.
// FSOps is the layer that knows about the filesystem schema and
// applies defaults at the boundary where they have to be visible
// to FUSE/WebDAV.
//
// Decode errors are silently swallowed: the same hot-path policy
// as fsmeta.Resolver inside View. A single bad metadata payload
// must not poison Stat/Listdir for the whole tree.
func (o *FSOps) fileInfoFromNode(n Node) FileInfo {
	fi := FileInfo{
		Name:    n.FS.Name,
		Path:    n.FS.Path,
		Size:    n.FS.Size,
		ModTime: n.FS.ModTime,
		IsDir:   n.FS.IsDir,
	}

	// For files, prefer fsmeta-encoded attributes when present.
	// For virtual directories, n.Artifact is nil — defaults are
	// the only available source.
	if n.Artifact != nil {
		if fs, ok, err := fsmeta.Decode(n.Artifact.Metadata); err == nil && ok {
			fi.Mode = fs.Mode
			fi.UID = fs.UID
			fi.GID = fs.GID
			if !fs.ModTime.IsZero() {
				fi.ModTime = fs.ModTime
			}
		}
	}

	if fi.Mode == 0 {
		if fi.IsDir {
			fi.Mode = 0o755
		} else {
			fi.Mode = o.defaultMode
		}
	}
	if fi.UID == 0 {
		fi.UID = o.defaultUID
	}
	if fi.GID == 0 {
		fi.GID = o.defaultGID
	}
	return fi
}

// --- readOnlyFile ---

// readOnlyFile wraps a core.ReadHandle in the File interface,
// returning ErrEditingDisabled for every write/sync method. The
// underlying handle's random-access support is propagated: ReadAt
// works iff the handle supports it.
type readOnlyFile struct {
	rh core.ReadHandle
}

func (f *readOnlyFile) ReadAt(p []byte, off int64) (int, error) {
	if !f.rh.SupportsRandomAccess() {
		// FSOps File contract requires ReadAt; fall back to the
		// stream-only error so callers can detect the situation
		// and degrade if they have an alternative path.
		return 0, fmt.Errorf("%w: read handle has no random access",
			errs.ErrArtifactUnreadable)
	}
	return f.rh.ReadAt(p, off)
}

func (f *readOnlyFile) WriteAt(p []byte, off int64) (int, error) {
	return 0, fmt.Errorf("%w: WriteAt on read-only handle",
		errs.ErrEditingDisabled)
}

func (f *readOnlyFile) Sync() error {
	// Sync on a read-only handle is a no-op on POSIX, but we
	// surface it as disabled to keep the semantics predictable —
	// any caller invoking Sync intends a write barrier.
	return fmt.Errorf("%w: Sync on read-only handle",
		errs.ErrEditingDisabled)
}

func (f *readOnlyFile) Truncate(size int64) error {
	return fmt.Errorf("%w: Truncate on read-only handle",
		errs.ErrEditingDisabled)
}

func (f *readOnlyFile) Close() error { return f.rh.Close() }

// --- pathLockManager ---

// pathLockManager is the per-path RWMutex registry used by FSOps
// to serialise mutating operations on a path while permitting
// concurrent readers.
//
// The map is never pruned: an FSOps instance accumulates one
// lock per unique path touched in its lifetime. For typical
// mount sessions the count stays in the thousands; pruning would
// require reference counting that is not worth the complexity
// at this stage.
type pathLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func newPathLockManager() *pathLockManager {
	return &pathLockManager{
		locks: make(map[string]*sync.RWMutex),
	}
}

// Get returns the RWMutex for path, creating one on first
// access. Stable: the same path always returns the same mutex.
func (m *pathLockManager) Get(path string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[path]
	if !ok {
		l = &sync.RWMutex{}
		m.locks[path] = l
	}
	return l
}

// lockOrdered locks every path in lex order so two concurrent
// callers locking the same set of paths cannot deadlock.
// Returns a single function that releases all of them in
// reverse order.
//
// Used by Rename (4b) which holds two paths simultaneously.
func (m *pathLockManager) lockOrdered(paths ...string) func() {
	if len(paths) == 0 {
		return func() {}
	}
	// Copy so we do not mutate the caller's slice.
	sorted := append([]string(nil), paths...)
	// Insertion sort for tiny slices — simpler than sort.Strings
	// for the common case (2 paths).
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	taken := make([]*sync.RWMutex, 0, len(sorted))
	for _, p := range sorted {
		l := m.Get(p)
		l.Lock()
		taken = append(taken, l)
	}
	return func() {
		for i := len(taken) - 1; i >= 0; i-- {
			taken[i].Unlock()
		}
	}
}

// Compile-time guards.
var (
	_ File = (*readOnlyFile)(nil)
)

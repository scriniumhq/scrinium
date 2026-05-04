package projection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
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

	store StoreClient

	scratchDir   string
	scratchQuota int64

	defaultMode uint32
	defaultUID  uint32
	defaultGID  uint32

	editing      EditingPolicy
	mountSession string
	namespace    string
	readOnly     bool

	// Path locks: per-path RWMutex serialising mutations on a
	// single path while permitting concurrent readers.
	pathLocks *pathLockManager

	// quota tracks the total bytes held across all live scratch
	// files. Reserve/Release pair around each WriteAt.
	quota *quotaTracker

	// pendingDirs holds virtual directories created via Mkdir
	// that have no children yet, so Stat/Listdir can see them
	// before any file lands inside. Cleared on Rmdir or when a
	// real child appears (see Listdir/Stat — pendingDirs are
	// not removed there; the directory simply moves into the
	// View when ensureDirs runs at Add time).
	pendingDirsMu sync.Mutex
	pendingDirs   map[string]struct{}
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
		quota:        &quotaTracker{quota: o.scratchQuota},
		pendingDirs:  make(map[string]struct{}),
	}, nil
}

// --- Read side ---

// Stat returns the FileInfo for a virtual path interpreted in the
// configured root tree.
//
// Stat also surfaces virtual directories created via Mkdir that
// have no children yet — without them, sequences like
// `mkdir foo && stat foo` would yield ENOENT for paths that the
// caller just created.
func (o *FSOps) Stat(path string) (FileInfo, error) {
	n, err := o.lookupInRoot(path)
	if err == nil {
		return o.fileInfoFromNode(n), nil
	}
	if !errors.Is(err, errs.ErrPathNotFound) {
		return FileInfo{}, err
	}
	// Fall back to pendingDirs (Mkdir-created, no real children).
	if o.isPendingDir(path) {
		return o.pendingDirInfo(path), nil
	}
	return FileInfo{}, err
}

// Listdir streams the immediate children of path. Returns
// ErrNotADirectory on a file, ErrPathNotFound on a missing path.
//
// Like Stat, Listdir surfaces Mkdir-created virtual directories
// even when they have no real children yet. The streamed
// children include both real (View-known) entries and pending
// directories whose parent matches path.
func (o *FSOps) Listdir(path string) FileInfoSeq {
	return func(yield func(FileInfo, error) bool) {
		// 1) Try the View first.
		seq := o.listInRoot(path)
		var listErr error
		yielded := false
		for n, err := range seq {
			if err != nil {
				listErr = err
				break
			}
			yielded = true
			if !yield(o.fileInfoFromNode(n), nil) {
				return
			}
		}
		// 2) When the View said "not found" but path is a pending
		//    dir, treat it as an empty directory and yield only
		//    pending children (if any).
		if listErr != nil && errors.Is(listErr, errs.ErrPathNotFound) && o.isPendingDir(path) {
			listErr = nil
		}
		if listErr != nil {
			yield(FileInfo{}, listErr)
			return
		}
		// 3) Append pending children whose parent equals path.
		for _, child := range o.pendingChildrenOf(path) {
			if !yield(child, nil) {
				return
			}
		}
		_ = yielded
	}
}

// Open returns a File handle. The mode bits select the access
// pattern:
//
//   - OpenReadOnly — read existing artifact via View.
//   - OpenWriteOnly / OpenReadWrite — open scratch buffer for
//     editing the existing artifact at path. Editing of an
//     existing artifact requires AllowSetattr or AllowTruncate
//     (4c); 4b only supports OpenReadWrite for newly-created
//     files (use Create for new files).
//   - OpenAppend — requires AllowAppend (4c).
//
// In stage 4b, Open with a write mode on an existing file
// returns ErrEditingDisabled — Create is the documented entry
// point for new files; Setattr/Truncate (4c) covers editing.
func (o *FSOps) Open(ctx context.Context, path string, mode OpenMode) (File, error) {
	if o.readOnly && mode != OpenReadOnly {
		return nil, fmt.Errorf("%w: write-mode Open on read-only FSOps",
			errs.ErrEditingDisabled)
	}
	if mode == OpenReadOnly {
		return o.openForRead(ctx, path)
	}
	// Append needs its own policy bit; treat as a separate path.
	if mode&OpenAppend != 0 {
		if !o.editing.AllowAppend {
			return nil, fmt.Errorf("%w: O_APPEND", errs.ErrEditingDisabled)
		}
		return o.openForAppend(ctx, path)
	}
	// Plain write/read-write on an existing file — editing. Allow
	// when any editing policy bit is set: the caller has already
	// expressed intent to mutate, and Setattr/Truncate plus
	// arbitrary writes are all reachable from this handle.
	if !o.editing.AllowSetattr && !o.editing.AllowTruncate && !o.editing.AllowAppend {
		return nil, fmt.Errorf("%w: write-mode Open requires editing policy",
			errs.ErrEditingDisabled)
	}
	return o.openForEdit(ctx, path, mode)
}

// openForEdit prepares a writeFile pre-loaded with the existing
// artifact's content and fsmeta, ready for arbitrary WriteAt /
// Truncate. On Close the result lands in the View through Move.
func (o *FSOps) openForEdit(ctx context.Context, path string, mode OpenMode) (File, error) {
	lock := o.pathLocks.Get(path)
	lock.Lock()

	wf, err := o.prepareEditingScratch(ctx, path)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	wf.unlock = lock.Unlock
	_ = mode // mode bits beyond editing presence have no effect on the handle
	return wf, nil
}

// openForAppend is the O_APPEND path. The implementation is
// identical to openForEdit (scratch pre-loaded with existing
// content); the caller writes at offsets >= current size.
//
// AllowAppend is independent of Setattr/Truncate so this path
// must work on its own. Setattr and Truncate operations from the
// returned handle are still gated by their respective policy
// bits at Close time? — no: the handle holds no per-op policy,
// it just performs whatever writes/truncates the caller dispatches.
// In practice O_APPEND callers only WriteAt at the end and Close.
func (o *FSOps) openForAppend(ctx context.Context, path string) (File, error) {
	lock := o.pathLocks.Get(path)
	lock.Lock()

	wf, err := o.prepareEditingScratch(ctx, path)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	wf.unlock = lock.Unlock
	return wf, nil
}

// openForRead is the stage-4a code path: pure View read.
func (o *FSOps) openForRead(ctx context.Context, path string) (File, error) {
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

// --- Write side ---

// Create makes a new file at path and returns a writable File
// handle. The handle buffers writes in a scratch file; on Close
// the scratch is consumed by Store.Put and the resulting
// manifest is added to the View.
//
// Errors:
//   - ErrInvalidPath if path fails fsmeta validation.
//   - ErrEditingDisabled if FSOps was constructed with WithReadOnly.
//   - "WithNamespace not configured" if Namespace is empty.
//   - "WithStore not configured" if no StoreClient was supplied.
//   - ErrPathExists wrapping the existing-path detail when the
//     target is already taken.
//
// Stage 4b only supports Create for new paths; opening an
// existing path for write lands in 4c.
func (o *FSOps) Create(ctx context.Context, path string, mode uint32) (File, error) {
	if o.readOnly {
		return nil, fmt.Errorf("%w: Create on read-only FSOps", errs.ErrEditingDisabled)
	}
	if err := fsmeta.ValidatePath(path); err != nil {
		return nil, err
	}
	if o.namespace == "" {
		return nil, fmt.Errorf("projection.FSOps.Create: WithNamespace not configured")
	}
	if o.store == nil {
		return nil, fmt.Errorf("projection.FSOps.Create: WithStore not configured")
	}
	if _, err := o.lookupInRoot(path); err == nil {
		return nil, fmt.Errorf("%w: %q already exists", errs.ErrPathExists, path)
	} else if !errors.Is(err, errs.ErrPathNotFound) {
		return nil, err
	}
	if o.isPendingDir(path) {
		return nil, fmt.Errorf("%w: %q already exists as a pending directory",
			errs.ErrPathExists, path)
	}

	// Lock the path for the lifetime of the handle. Released in
	// writeFile.Close (or by the caller via the rollback path on
	// errors below).
	lock := o.pathLocks.Get(path)
	lock.Lock()

	// Open scratch file. We do NOT pre-reserve quota here — the
	// quota check happens per WriteAt against the running scratch
	// size. A Create-then-Close with no Write is a no-op (returns
	// nil, scratch deleted, no Put).
	scratchPath, scratchFile, err := o.newScratchFile()
	if err != nil {
		lock.Unlock()
		return nil, err
	}

	return &writeFile{
		fsops:       o,
		path:        path,
		scratchPath: scratchPath,
		handle:      scratchFile,
		mode:        mode,
		unlock:      lock.Unlock,
	}, nil
}

// Unlink deletes the artifact at path. The View entry is removed
// after a successful Store.Delete.
//
// Errors:
//   - ErrEditingDisabled if FSOps is read-only.
//   - ErrPathNotFound if path is unknown to the View.
//   - ErrIsADirectory if path is a virtual directory; use Rmdir.
//   - Any error from Store.Delete (e.g. ErrLocked, ErrRetentionActive)
//     is propagated.
func (o *FSOps) Unlink(ctx context.Context, path string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Unlink on read-only FSOps", errs.ErrEditingDisabled)
	}
	if o.store == nil {
		return fmt.Errorf("projection.FSOps.Unlink: WithStore not configured")
	}

	lock := o.pathLocks.Get(path)
	lock.Lock()
	defer lock.Unlock()

	n, err := o.lookupInRoot(path)
	if err != nil {
		return err
	}
	if n.FS.IsDir {
		return fmt.Errorf("%w: %q", errs.ErrIsADirectory, path)
	}
	id := n.Artifact.ArtifactID
	if err := o.store.Delete(ctx, id); err != nil {
		return err
	}
	if err := o.view.Remove(id); err != nil {
		// View.Remove failures (e.g. ErrViewClosed) leave the
		// store in a consistent state — the artifact is gone.
		// Surface the error so the caller can decide whether to
		// retry.
		return err
	}
	return nil
}

// Mkdir creates a virtual directory at path. The directory is
// "pending" until a real artifact is created inside it; until
// then it is visible only through Stat/Listdir on this FSOps
// (it does not exist in any tree of the View).
//
// Errors:
//   - ErrEditingDisabled if FSOps is read-only.
//   - ErrInvalidPath if path fails validation.
//   - ErrPathExists if path is already taken (real or pending).
func (o *FSOps) Mkdir(path string, mode uint32) error {
	if o.readOnly {
		return fmt.Errorf("%w: Mkdir on read-only FSOps", errs.ErrEditingDisabled)
	}
	if err := fsmeta.ValidatePath(path); err != nil {
		return err
	}
	if _, err := o.lookupInRoot(path); err == nil {
		return fmt.Errorf("%w: %q already exists", errs.ErrPathExists, path)
	} else if !errors.Is(err, errs.ErrPathNotFound) {
		return err
	}
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	if _, ok := o.pendingDirs[path]; ok {
		return fmt.Errorf("%w: %q is a pending directory", errs.ErrPathExists, path)
	}
	o.pendingDirs[path] = struct{}{}
	_ = mode // POSIX mode for virtual dirs is not stored; FSOps default applies
	return nil
}

// Rmdir removes a directory.
//
// Behaviour:
//   - For a pending directory (Mkdir-created, no real children) —
//     drop it from pendingDirs.
//   - For a virtual directory in the View — succeed if empty
//     (no children in the tree), otherwise ErrNotEmpty.
//   - On a file path — ErrNotADirectory.
//   - On an unknown path — ErrPathNotFound.
//
// Removing a virtual directory from the View has no persistent
// effect: the directory exists by virtue of having children, and
// a successful Rmdir on an already-empty view-dir is a no-op
// outside the FSOps's own state. Future Adds re-create it
// automatically through ensureDirs.
func (o *FSOps) Rmdir(path string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Rmdir on read-only FSOps", errs.ErrEditingDisabled)
	}
	o.pendingDirsMu.Lock()
	if _, ok := o.pendingDirs[path]; ok {
		delete(o.pendingDirs, path)
		o.pendingDirsMu.Unlock()
		return nil
	}
	o.pendingDirsMu.Unlock()

	n, err := o.lookupInRoot(path)
	if err != nil {
		return err
	}
	if !n.FS.IsDir {
		return fmt.Errorf("%w: %q", errs.ErrNotADirectory, path)
	}
	// Check emptiness via Listdir.
	for _, lerr := range o.listInRoot(path) {
		if lerr != nil {
			return lerr
		}
		return fmt.Errorf("%w: %q", errs.ErrNotEmpty, path)
	}
	// Empty view-dir: no persistent action — the dir exists by
	// virtue of children.
	return nil
}

// --- Editing existing artifacts ---

// Rename moves an artifact from oldPath to newPath. In CAS terms
// the operation is a Put-with-new-fsmeta-Path followed by a
// Delete of the old artifact, atomically reflected in the View
// via View.Move.
//
// Errors:
//   - ErrEditingDisabled if AllowRename is off or FSOps is read-only.
//   - ErrInvalidPath if newPath fails validation.
//   - ErrPathNotFound if oldPath does not exist.
//   - ErrIsADirectory if oldPath points at a virtual directory.
//   - ErrPathExists if newPath is already taken.
//   - Any error from Store.Put / Store.Delete.
func (o *FSOps) Rename(ctx context.Context, oldPath, newPath string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Rename on read-only FSOps", errs.ErrEditingDisabled)
	}
	if !o.editing.AllowRename {
		return fmt.Errorf("%w: Rename without AllowRename", errs.ErrEditingDisabled)
	}
	if err := fsmeta.ValidatePath(newPath); err != nil {
		return err
	}
	if oldPath == newPath {
		return nil
	}
	if o.store == nil {
		return fmt.Errorf("projection.FSOps.Rename: WithStore not configured")
	}

	unlock := o.pathLocks.lockOrdered(oldPath, newPath)
	defer unlock()

	// newPath must not exist (file or pending dir).
	if _, err := o.lookupInRoot(newPath); err == nil {
		return fmt.Errorf("%w: %q already exists", errs.ErrPathExists, newPath)
	} else if !errors.Is(err, errs.ErrPathNotFound) {
		return err
	}
	if o.isPendingDir(newPath) {
		return fmt.Errorf("%w: %q is a pending directory", errs.ErrPathExists, newPath)
	}

	// Stage the old artifact's content and fsmeta into a scratch
	// editing handle whose Close performs Put+Delete+Move.
	wf, err := o.prepareEditingScratch(ctx, oldPath)
	if err != nil {
		return err
	}
	wf.path = newPath
	wf.forceDirty = true // content unchanged; metadata change alone triggers Put
	// Lock has already been taken by lockOrdered; substitute the
	// closer used by the writeFile so it does not double-unlock.
	wf.unlock = func() {} // unlock is handled by the deferred lockOrdered

	return wf.Close()
}

// Setattr changes POSIX attributes (mode, uid, gid, mtime) of an
// existing artifact. Each non-nil field of attrs is applied;
// other fsmeta fields (Path, MIME) are preserved. The operation
// produces a new artifact with the same content (the underlying
// blob is deduplicated by the Store) and removes the old.
//
// Errors mirror Rename, plus ErrEditingDisabled when AllowSetattr
// is off.
func (o *FSOps) Setattr(ctx context.Context, path string, attrs Attrs) error {
	if o.readOnly {
		return fmt.Errorf("%w: Setattr on read-only FSOps", errs.ErrEditingDisabled)
	}
	if !o.editing.AllowSetattr {
		return fmt.Errorf("%w: Setattr without AllowSetattr", errs.ErrEditingDisabled)
	}
	if o.store == nil {
		return fmt.Errorf("projection.FSOps.Setattr: WithStore not configured")
	}

	lock := o.pathLocks.Get(path)
	lock.Lock()
	defer lock.Unlock()

	wf, err := o.prepareEditingScratch(ctx, path)
	if err != nil {
		return err
	}
	wf.unlock = func() {} // already locked; release in our defer

	if attrs.Mode != nil {
		wf.inheritedFsmeta.Mode = *attrs.Mode
		wf.mode = *attrs.Mode // also influence Close's fsm.Mode override path
	}
	if attrs.UID != nil {
		wf.inheritedFsmeta.UID = *attrs.UID
	}
	if attrs.GID != nil {
		wf.inheritedFsmeta.GID = *attrs.GID
	}
	if attrs.ModTime != nil {
		wf.inheritedFsmeta.ModTime = *attrs.ModTime
	}
	wf.forceDirty = true

	return wf.Close()
}

// Truncate adjusts the size of an existing artifact. The new
// file is materialised by reading the existing content, capping
// at size (or extending with zeros if size > current), and
// writing a new artifact. The old is removed.
//
// Errors mirror Rename, plus ErrEditingDisabled when
// AllowTruncate is off, plus ErrScratchQuota if the scratch
// pre-allocation would exceed the quota.
func (o *FSOps) Truncate(ctx context.Context, path string, size int64) error {
	if o.readOnly {
		return fmt.Errorf("%w: Truncate on read-only FSOps", errs.ErrEditingDisabled)
	}
	if !o.editing.AllowTruncate {
		return fmt.Errorf("%w: Truncate without AllowTruncate", errs.ErrEditingDisabled)
	}
	if size < 0 {
		return fmt.Errorf("projection.FSOps.Truncate: negative size %d", size)
	}
	if o.store == nil {
		return fmt.Errorf("projection.FSOps.Truncate: WithStore not configured")
	}

	lock := o.pathLocks.Get(path)
	lock.Lock()
	defer lock.Unlock()

	wf, err := o.prepareEditingScratch(ctx, path)
	if err != nil {
		return err
	}
	wf.unlock = func() {}

	// Apply the size change to the scratch.
	if err := wf.Truncate(size); err != nil {
		// On quota failure / other error, abort: discard scratch
		// without Put.
		_ = wf.Close()
		return err
	}
	wf.forceDirty = true

	return wf.Close()
}

// prepareEditingScratch assembles a writeFile for editing the
// artifact at path: it allocates a scratch file, copies the
// existing content into it, decodes the existing fsmeta, and
// returns the handle ready for further mutation by the caller.
//
// Caller responsibilities (filled in after the call):
//   - wf.unlock — overwrite if the caller manages locks externally.
//   - wf.path / wf.mode / wf.inheritedFsmeta — mutate as the
//     editing operation requires.
//   - wf.forceDirty — set to true when no WriteAt will follow
//     (Setattr, Rename) so Close still performs a Put.
//
// On error the scratch is fully cleaned up.
func (o *FSOps) prepareEditingScratch(ctx context.Context, path string) (*writeFile, error) {
	n, err := o.lookupInRoot(path)
	if err != nil {
		return nil, err
	}
	if n.FS.IsDir {
		return nil, fmt.Errorf("%w: %q", errs.ErrIsADirectory, path)
	}

	// Decode old fsmeta to inherit non-mutated fields. A clean
	// failure here (no fsmeta on the artifact) is acceptable —
	// the inherited struct stays zero, and Close re-encodes from
	// scratch; the artifact gains a fresh fsmeta with only the
	// mutated fields plus path.
	var inherited fsmeta.FileSystem
	if n.Artifact != nil {
		if fs, ok, _ := fsmeta.Decode(n.Artifact.Metadata); ok {
			inherited = fs
		}
	}

	scratchPath, scratchFile, err := o.newScratchFile()
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		scratchFile.Close()
		os.Remove(scratchPath)
	}

	// Copy content from the existing artifact into the scratch.
	rh, err := o.openInRoot(ctx, path)
	if err != nil {
		cleanup()
		return nil, err
	}
	written, err := io.Copy(scratchFile, rh)
	rh.Close()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("projection.FSOps: stage scratch: %w", err)
	}
	// Reserve quota for the staged bytes. If the quota is
	// exceeded we fail before the caller has a chance to mutate.
	if err := o.quota.Reserve(written); err != nil {
		cleanup()
		return nil, err
	}

	return &writeFile{
		fsops:             o,
		path:              path,
		scratchPath:       scratchPath,
		handle:            scratchFile,
		mode:              inherited.Mode,
		unlock:            func() {}, // caller-managed by default
		replaceArtifactID: n.Artifact.ArtifactID,
		oldPath:           path,
		inheritedFsmeta:   inherited,
		size:              written,
	}, nil
}

// --- Pending directories ---

func (o *FSOps) isPendingDir(path string) bool {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	_, ok := o.pendingDirs[path]
	return ok
}

// pendingDirInfo synthesises a FileInfo for a pending directory.
// Mode comes from FSOps default for directories (0755).
func (o *FSOps) pendingDirInfo(path string) FileInfo {
	return FileInfo{
		Name:  lastPathSegment(path),
		Path:  path,
		IsDir: true,
		Mode:  0o755,
		UID:   o.defaultUID,
		GID:   o.defaultGID,
	}
}

// pendingChildrenOf returns FileInfos for pending directories
// whose parent equals parent. Order is sorted by Name.
func (o *FSOps) pendingChildrenOf(parent string) []FileInfo {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	var out []FileInfo
	for p := range o.pendingDirs {
		if parentOfPath(p) != parent {
			continue
		}
		out = append(out, FileInfo{
			Name:  lastPathSegment(p),
			Path:  p,
			IsDir: true,
			Mode:  0o755,
			UID:   o.defaultUID,
			GID:   o.defaultGID,
		})
	}
	// Sort for deterministic order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Name > out[j].Name; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// parentOfPath returns the parent directory of a slash-separated
// path. parentOfPath("a/b/c") == "a/b"; parentOfPath("a") == "".
func parentOfPath(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

// lastPathSegment returns the final segment of a slash-separated
// path. lastPathSegment("a/b/c") == "c"; lastPathSegment("a") == "a".
func lastPathSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// --- Scratch handling ---

// newScratchFile creates a fresh scratch file in the configured
// directory. Returns the absolute path and the open *os.File.
func (o *FSOps) newScratchFile() (string, *os.File, error) {
	dir := o.scratchDir
	if dir == "" {
		// Without an explicit scratch dir, use the OS temp dir.
		// Production callers always set this; tests may rely on
		// the default.
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("projection.FSOps: mkdir scratch: %w", err)
	}
	f, err := os.CreateTemp(dir, "scratch-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("projection.FSOps: create scratch: %w", err)
	}
	return f.Name(), f, nil
}

// --- writeFile ---

// writeFile is a write-side File handle backed by an OS scratch
// file. WriteAt drains into the scratch and bumps the running
// quota; Close turns the scratch into a Store.Put and updates
// the View.
//
// Editing existing artifacts: when replaceArtifactID is non-empty,
// Close treats the operation as a replace — after the new Put it
// also calls Store.Delete(replaceArtifactID) and uses View.Move
// instead of View.Add. inheritedFsmeta carries the fsmeta of the
// pre-existing artifact so callers (Setattr, Rename) can inherit
// fields they don't explicitly mutate.
//
// Locks: a single-path Open holds one lock; Rename holds two
// (old + new) acquired in lex order via pathLocks.lockOrdered.
// The unlock function lives in `unlock` and is called once on
// Close regardless of which path produced the lock.
type writeFile struct {
	fsops       *FSOps
	path        string
	scratchPath string
	handle      *os.File
	mode        uint32

	// unlock releases the path-level lock(s) held by this
	// handle. Set by the constructor (Create or open-for-edit
	// helpers); always called exactly once at Close end.
	unlock func()

	// Editing fields.
	replaceArtifactID domain.ArtifactID // empty for new files
	oldPath           string            // empty for new files
	inheritedFsmeta   fsmeta.FileSystem // base for fsmeta on Close

	// markDirty=true forces Close to perform a Put even when no
	// WriteAt happened. Used by Setattr/Rename where the content
	// is unchanged but metadata has to be re-emitted as a new
	// artifact.
	forceDirty bool

	mu     sync.Mutex
	size   int64 // logical scratch size as the writer sees it
	dirty  bool  // any successful WriteAt
	closed bool
}

// ReadAt reads from the scratch file. Useful for OpenReadWrite
// flows but in 4b primarily exists to satisfy the File contract.
func (f *writeFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fmt.Errorf("projection.FSOps: file closed")
	}
	return f.handle.ReadAt(p, off)
}

// WriteAt drains data into the scratch at offset off. The quota
// is reserved against the *new* logical size, so a Write that
// would push total scratch usage above the quota returns
// ErrScratchQuota without touching the file.
func (f *writeFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fmt.Errorf("projection.FSOps: file closed")
	}
	newEnd := off + int64(len(p))
	delta := newEnd - f.size
	if delta < 0 {
		delta = 0
	}
	if err := f.fsops.quota.Reserve(delta); err != nil {
		return 0, err
	}
	n, err := f.handle.WriteAt(p, off)
	if err != nil {
		// Roll back the reservation; the WriteAt may have
		// partially succeeded — n bytes are on disk, but we
		// account for the full delta because the caller will
		// see the error and likely close.
		f.fsops.quota.Release(delta)
		return n, err
	}
	if newEnd > f.size {
		f.size = newEnd
	}
	if n > 0 {
		f.dirty = true
	}
	return n, nil
}

// Truncate adjusts the scratch size. Stage 4b only allows
// truncating *new* files (the writeFile owns the scratch from
// Create); editing an existing file's size requires AllowTruncate
// and lives in 4c.
func (f *writeFile) Truncate(size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fmt.Errorf("projection.FSOps: file closed")
	}
	if size > f.size {
		// Reserve the growth against the quota.
		if err := f.fsops.quota.Reserve(size - f.size); err != nil {
			return err
		}
	} else if size < f.size {
		f.fsops.quota.Release(f.size - size)
	}
	if err := f.handle.Truncate(size); err != nil {
		return err
	}
	f.size = size
	f.dirty = true
	return nil
}

// Sync flushes the scratch to the OS. The scratch is not yet a
// Store artifact; Sync here is purely about durability of the
// in-progress write buffer.
func (f *writeFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fmt.Errorf("projection.FSOps: file closed")
	}
	return f.handle.Sync()
}

// Close finalises the handle. Behaviour depends on dirty and on
// whether the handle is editing an existing artifact:
//
//   - Clean (no successful WriteAt and no forceDirty): scratch is
//     deleted, no Put, the path is left untouched.
//   - Dirty + new file: Store.Put -> Store.Get -> View.Add.
//   - Dirty + editing (replaceArtifactID set): Store.Put ->
//     Store.Delete(replaceArtifactID) -> Store.Get -> View.Move.
//
// Quota reserved during writes is released either way. The path
// lock(s) are released last via the unlock closure.
func (f *writeFile) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	dirty := f.dirty || f.forceDirty
	size := f.size
	f.mu.Unlock()

	defer f.unlock()
	defer f.fsops.quota.Release(size)
	defer os.Remove(f.scratchPath)
	defer f.handle.Close()

	if !dirty {
		return nil
	}
	// Rewind the scratch so Store.Put can read from offset 0.
	if _, err := f.handle.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("projection.FSOps: seek scratch: %w", err)
	}

	// Build fsmeta. For editing, start from the inherited fsmeta
	// (preserves MIME, plus any field not explicitly mutated by
	// the caller) and overlay the new path/mode. For new files,
	// inheritedFsmeta is the zero value.
	fsm := f.inheritedFsmeta
	fsm.Path = f.path
	if f.mode != 0 {
		fsm.Mode = f.mode
	}
	// ModTime: for new files (no predecessor), stamp with the
	// current time. For editing, the caller has already placed
	// the desired ModTime into inheritedFsmeta — Setattr writes
	// the user's value there explicitly, Rename inherits the old
	// artifact's value, and an arbitrary write through
	// openForEdit also keeps the inherited value. Overwriting
	// here would clobber Setattr's intent.
	if f.replaceArtifactID == "" {
		fsm.ModTime = time.Now().UTC()
	}

	metadata, err := fsmeta.Encode(fsm)
	if err != nil {
		return err
	}

	id, err := f.fsops.store.Put(
		context.Background(),
		domain.Artifact{
			Payload:  f.handle,
			Metadata: metadata,
		},
		domain.PutOptions{
			SessionID: f.fsops.mountSession,
			Namespace: f.fsops.namespace,
			BlobType:  domain.BlobTypeRegular,
		},
	)
	if err != nil {
		return err
	}

	// For editing paths, drop the predecessor before refetching
	// the new manifest. If Delete fails the new artifact is
	// already in place; surface the error so the caller can
	// observe the partial state — a subsequent reconciliation
	// (e.g. GC) will eventually drop the orphan.
	if f.replaceArtifactID != "" {
		if err := f.fsops.store.Delete(context.Background(), f.replaceArtifactID); err != nil {
			return fmt.Errorf("projection.FSOps: delete predecessor: %w", err)
		}
	}

	// Fetch the resulting manifest to update the View.
	rh, err := f.fsops.store.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		return fmt.Errorf("projection.FSOps: refetch new manifest: %w", err)
	}
	manifest := rh.Manifest()
	rh.Close()

	if f.replaceArtifactID != "" {
		// Editing: Move handles both removal of the old by-path
		// owner (which Store.Delete already enforced separately)
		// and addition of the new manifest in every tree.
		if err := f.fsops.view.Move(f.oldPath, f.path, manifest); err != nil {
			return err
		}
	} else {
		if err := f.fsops.view.Add(manifest); err != nil {
			return err
		}
	}

	// If the new file lives inside a pending directory, the
	// pending entry is now redundant (View.Add/Move ran
	// ensureDirs). Drop the entry to keep state tidy.
	f.fsops.dropParentPendingDirs(f.path)

	return nil
}

// dropParentPendingDirs removes pendingDirs entries that match
// any ancestor of path. Called after a successful Add to clean
// up "pre-created" directories now backed by real children.
func (o *FSOps) dropParentPendingDirs(path string) {
	o.pendingDirsMu.Lock()
	defer o.pendingDirsMu.Unlock()
	for p := range o.pendingDirs {
		// Trim entries that are an ancestor of path or equal.
		if p == "" {
			continue
		}
		if path == p || hasParentPath(path, p) {
			delete(o.pendingDirs, p)
		}
	}
}

// hasParentPath reports whether parent is a strict ancestor of
// child. hasParentPath("a/b/c", "a/b") == true; ("a/b", "a/b") ==
// false.
func hasParentPath(child, parent string) bool {
	if parent == "" {
		return child != ""
	}
	if len(child) <= len(parent) {
		return false
	}
	if child[:len(parent)] != parent {
		return false
	}
	return child[len(parent)] == '/'
}

// --- quotaTracker ---

// quotaTracker enforces a global cap on the total bytes held by
// all live scratch files of an FSOps instance. Reserve grows the
// counter; Release shrinks it. quota == 0 disables the cap.
type quotaTracker struct {
	mu    sync.Mutex
	used  int64
	quota int64
}

// Reserve raises the counter by n. Returns ErrScratchQuota if
// quota is enabled and the new total would exceed it. Negative n
// is treated as zero (no quota effect; matches WriteAt which
// passes max(0, growth)).
func (q *quotaTracker) Reserve(n int64) error {
	if n <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.quota > 0 && q.used+n > q.quota {
		return fmt.Errorf("%w: requested %d, used %d, cap %d",
			errs.ErrScratchQuota, n, q.used, q.quota)
	}
	q.used += n
	return nil
}

// Release shrinks the counter by n. Bottoms at zero (defensive —
// double-release should not corrupt accounting).
func (q *quotaTracker) Release(n int64) {
	if n <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used -= n
	if q.used < 0 {
		q.used = 0
	}
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

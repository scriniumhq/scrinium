package fsops

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
	"scrinium.dev/internal/keyedlock"
	vw "scrinium.dev/projection/internal/view"
)

// Ops is the filesystem-shaped operations layer over a View.
// It serves transports (FUSE, WebDAV) by translating
// path-keyed POSIX-like calls into View lookups (read side) and,
// in stage 4b, into Store mutations (write side).
//
// The two reasons Ops exists rather than the transport calling
// View directly:
//
//  1. Editing policy, scratch buffering, path-level locks live in
//     one place — FUSE and WebDAV inherit them by construction.
//  2. The transport works in terms of the configured root tree
//     (RootView). Ops hides that routing from the caller.
//
// Stage 4a: read-side (Stat, Listdir, Open). Mutations land in 4b.
type Ops struct {
	view *vw.View

	store StoreClient

	scratchDir   string
	scratchQuota int64

	defaultMode uint32
	defaultUID  uint32
	defaultGID  uint32

	editing      EditingPolicy
	mountSession domain.SessionID
	readOnly     bool

	// Path locks: per-path RWMutex serialising mutations on a
	// single path while permitting concurrent readers.
	pathLocks *keyedlock.Map

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

const (
	OpenReadOnly  OpenMode = 0
	OpenWriteOnly OpenMode = 1
	OpenReadWrite OpenMode = 2
	OpenAppend    OpenMode = 4
)

// New wraps a View with filesystem-shaped operations.
//
// The View must already exist (the caller is responsible for the
// View's lifecycle); New does not call view.New itself
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
func New(v *vw.View, opts ...Option) (*Ops, error) {
	if v == nil {
		return nil, fmt.Errorf("projection.New: view is nil")
	}
	o := fsOpsOptions{
		defaultMode: 0o644,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return &Ops{
		view:         v,
		store:        o.store,
		scratchDir:   o.scratchDir,
		scratchQuota: o.scratchQuota,
		defaultMode:  o.defaultMode,
		defaultUID:   o.defaultUID,
		defaultGID:   o.defaultGID,
		editing:      o.editing,
		mountSession: o.mountSession,
		readOnly:     o.readOnly,
		pathLocks:    keyedlock.New(),
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
func (o *Ops) Stat(path string) (FileInfo, error) {
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
func (o *Ops) Listdir(path string) FileInfoSeq {
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
func (o *Ops) Open(ctx context.Context, path string, mode OpenMode) (File, error) {
	if o.readOnly && mode != OpenReadOnly {
		return nil, fmt.Errorf("%w: write-mode Open on read-only Ops",
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
// artifact's content and vfsmeta, ready for arbitrary WriteAt /
// Truncate. On Close the result lands in the View through Move.
func (o *Ops) openForEdit(ctx context.Context, path string, mode OpenMode) (File, error) {
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
func (o *Ops) openForAppend(ctx context.Context, path string) (File, error) {
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
func (o *Ops) openForRead(ctx context.Context, path string) (File, error) {
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
func (o *Ops) lookupInRoot(path string) (vw.Node, error) {
	return o.view.GetIn(o.view.RootView(), path)
}

func (o *Ops) listInRoot(path string) vw.Seq {
	return o.view.ListIn(o.view.RootView(), path)
}

func (o *Ops) openInRoot(ctx context.Context, path string) (domain.ReadHandle, error) {
	return o.view.OpenIn(ctx, o.view.RootView(), path)
}

// fileInfoFromNode converts a Node into a FileInfo, applying
// Ops defaults to fields the artifact left zero.
//
// For file nodes, Ops reads vfsmeta from Manifest.Ext directly
// — the View itself is schema-agnostic and does not surface
// vfsmeta.Mode/UID/GID/ModTime through FilesystemFacet. Ops is
// the layer that knows about the filesystem schema and applies
// defaults at the boundary where they have to be visible to
// FUSE/WebDAV.
//
// Decode errors are silently swallowed: the same hot-path policy
// as vfsmeta.Resolver inside View. A single bad ext payload must
// not poison Stat/Listdir for the whole tree.
func (o *Ops) fileInfoFromNode(n vw.Node) FileInfo {
	fi := FileInfo{
		Name:    n.FS.Name,
		Path:    n.FS.Path,
		Size:    n.FS.Size,
		ModTime: n.FS.ModTime,
		IsDir:   n.FS.IsDir,
	}

	// For files, prefer vfsmeta-encoded attributes when present.
	// For virtual directories, n.Artifact is nil — defaults are
	// the only available source.
	if n.Artifact != nil {
		fi.ArtifactID = n.Artifact.ArtifactID
		if fs, ok, err := vfsmeta.Decode(n.Artifact.Ext); err == nil && ok {
			fi.Mode = fs.Mode
			fi.UID = fs.UID
			fi.GID = fs.GID
			fi.MIME = fs.MIME
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
//   - ErrInvalidPath if path fails vfsmeta validation.
//   - ErrEditingDisabled if Ops was constructed with WithReadOnly.
//   - "WithStore not configured" if no StoreClient was supplied.
//   - ErrPathExists wrapping the existing-path detail when the
//     target is already taken.
//
// Stage 4b only supports Create for new paths; opening an
// existing path for write lands in 4c.
func (o *Ops) Create(ctx context.Context, path string, mode uint32) (File, error) {
	if o.readOnly {
		return nil, fmt.Errorf("%w: Create on read-only Ops", errs.ErrEditingDisabled)
	}
	if err := vfsmeta.ValidatePath(path); err != nil {
		return nil, err
	}
	if o.store == nil {
		return nil, fmt.Errorf("projection.Ops.Create: WithStore not configured")
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
//   - ErrEditingDisabled if Ops is read-only.
//   - ErrPathNotFound if path is unknown to the View.
//   - ErrIsADirectory if path is a virtual directory; use Rmdir.
//   - Any error from Store.Delete (e.g. ErrLocked, ErrRetentionActive)
//     is propagated.
func (o *Ops) Unlink(ctx context.Context, path string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Unlink on read-only Ops", errs.ErrEditingDisabled)
	}
	if o.store == nil {
		return fmt.Errorf("projection.Ops.Unlink: WithStore not configured")
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
// then it is visible only through Stat/Listdir on this Ops
// (it does not exist in any tree of the View).
//
// Errors:
//   - ErrEditingDisabled if Ops is read-only.
//   - ErrInvalidPath if path fails validation.
//   - ErrPathExists if path is already taken (real or pending).
func (o *Ops) Mkdir(path string, mode uint32) error {
	if o.readOnly {
		return fmt.Errorf("%w: Mkdir on read-only Ops", errs.ErrEditingDisabled)
	}
	if err := vfsmeta.ValidatePath(path); err != nil {
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
	_ = mode // POSIX mode for virtual dirs is not stored; Ops default applies
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
// outside the Ops's own state. Future Adds re-create it
// automatically through ensureDirs.
func (o *Ops) Rmdir(path string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Rmdir on read-only Ops", errs.ErrEditingDisabled)
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
// the operation is a Put-with-new-vfsmeta-Path followed by a
// Delete of the old artifact, atomically reflected in the View
// via View.Move.
//
// Errors:
//   - ErrEditingDisabled if AllowRename is off or Ops is read-only.
//   - ErrInvalidPath if newPath fails validation.
//   - ErrPathNotFound if oldPath does not exist.
//   - ErrIsADirectory if oldPath points at a virtual directory.
//   - ErrPathExists if newPath is already taken.
//   - Any error from Store.Put / Store.Delete.
func (o *Ops) Rename(ctx context.Context, oldPath, newPath string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Rename on read-only Ops", errs.ErrEditingDisabled)
	}
	if !o.editing.AllowRename {
		return fmt.Errorf("%w: Rename without AllowRename", errs.ErrEditingDisabled)
	}
	if err := vfsmeta.ValidatePath(newPath); err != nil {
		return err
	}
	if oldPath == newPath {
		return nil
	}
	if o.store == nil {
		return fmt.Errorf("projection.Ops.Rename: WithStore not configured")
	}

	unlock := o.pathLocks.LockAll(oldPath, newPath)
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

	// Stage the old artifact's content and vfsmeta into a scratch
	// editing handle whose Close performs Put+Delete+Move.
	wf, err := o.prepareEditingScratch(ctx, oldPath)
	if err != nil {
		return err
	}
	wf.path = newPath
	wf.forceDirty = true // content unchanged; metadata change alone triggers Put
	// Lock has already been taken by LockAll; substitute the
	// closer used by the writeFile so it does not double-unlock.
	wf.unlock = func() {} // unlock is handled by the deferred LockAll

	return wf.Close()
}

// Setattr changes POSIX attributes (mode, uid, gid, mtime) of an
// existing artifact. Each non-nil field of attrs is applied;
// other vfsmeta fields (Path, MIME) are preserved. The operation
// produces a new artifact with the same content (the underlying
// blob is deduplicated by the Store) and removes the old.
//
// Errors mirror Rename, plus ErrEditingDisabled when AllowSetattr
// is off.
func (o *Ops) Setattr(ctx context.Context, path string, attrs Attrs) error {
	if o.readOnly {
		return fmt.Errorf("%w: Setattr on read-only Ops", errs.ErrEditingDisabled)
	}
	if !o.editing.AllowSetattr {
		return fmt.Errorf("%w: Setattr without AllowSetattr", errs.ErrEditingDisabled)
	}
	if o.store == nil {
		return fmt.Errorf("projection.Ops.Setattr: WithStore not configured")
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
		wf.inheritedVfsmeta.Mode = *attrs.Mode
		wf.mode = *attrs.Mode // also influence Close's fsm.Mode override path
	}
	if attrs.UID != nil {
		wf.inheritedVfsmeta.UID = *attrs.UID
	}
	if attrs.GID != nil {
		wf.inheritedVfsmeta.GID = *attrs.GID
	}
	if attrs.ModTime != nil {
		wf.inheritedVfsmeta.ModTime = *attrs.ModTime
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
func (o *Ops) Truncate(ctx context.Context, path string, size int64) error {
	if o.readOnly {
		return fmt.Errorf("%w: Truncate on read-only Ops", errs.ErrEditingDisabled)
	}
	if !o.editing.AllowTruncate {
		return fmt.Errorf("%w: Truncate without AllowTruncate", errs.ErrEditingDisabled)
	}
	if size < 0 {
		return fmt.Errorf("projection.Ops.Truncate: negative size %d", size)
	}
	if o.store == nil {
		return fmt.Errorf("projection.Ops.Truncate: WithStore not configured")
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

// Compile-time guards.
var (
	_ File = (*readOnlyFile)(nil)
)

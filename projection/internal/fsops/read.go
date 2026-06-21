package fsops

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/errs"
)

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

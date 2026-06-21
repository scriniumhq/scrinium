package fsops

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
)

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

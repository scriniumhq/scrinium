package fsops

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
)

// --- Editing existing artifacts ---

// Rename moves oldPath to newPath. What it does depends on oldPath:
//
//   - A file: Put-with-new-vfsmeta-Path then Delete of the old artifact,
//     reflected in the View via Move. Gated by AllowRename.
//   - An empty pending directory: a pure namespace re-key (no Store touch),
//     carrying any nested pending dirs. Gated by AllowDirRename.
//   - A non-empty (view-backed) directory: a recursive re-path — one file
//     rename per descendant, plus nested pending dirs. Gated by AllowDirRename.
//     See renameDirTree.
//
// Mkdir/Rmdir stay on read-only alone; only renaming a directory crosses into
// the editing surface, which is why directory rename has its own policy bit.
//
// Errors:
//   - ErrEditingDisabled if Ops is read-only, or the matching policy bit
//     (AllowRename for files, AllowDirRename for directories) is off.
//   - ErrInvalidPath if newPath fails validation.
//   - ErrPathNotFound if oldPath does not exist.
//   - ErrPathExists if newPath is already taken.
//   - Any error from Store.Put / Store.Delete.
func (o *Ops) Rename(ctx context.Context, oldPath, newPath string) error {
	if o.readOnly {
		return fmt.Errorf("%w: Rename on read-only Ops", errs.ErrEditingDisabled)
	}

	// Directory rename — empty pending dir or recursive non-empty dir — is one
	// capability under AllowDirRename, separate from file rename's AllowRename
	// (the recursive form rewrites every descendant's manifest).
	if o.isPendingDir(oldPath) {
		if !o.editing.AllowDirRename {
			return fmt.Errorf("%w: directory rename without AllowDirRename", errs.ErrEditingDisabled)
		}
		return o.renamePendingDir(oldPath, newPath)
	}
	if o.isViewDir(oldPath) {
		if !o.editing.AllowDirRename {
			return fmt.Errorf("%w: directory rename without AllowDirRename", errs.ErrEditingDisabled)
		}
		return o.renameDirTree(ctx, oldPath, newPath)
	}

	// File rename. A missing oldPath falls here too and surfaces ErrPathNotFound
	// from the staging lookup below.
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

	return o.renameArtifactLocked(ctx, oldPath, newPath)
}

// renameArtifactLocked re-stamps the single artifact at oldPath with newPath:
// stage its content+vfsmeta into a scratch handle whose Close performs
// Put(new path)+Delete(old)+View.Move. The caller holds the path locks for
// oldPath and newPath and has verified newPath is free.
func (o *Ops) renameArtifactLocked(ctx context.Context, oldPath, newPath string) error {
	wf, err := o.prepareEditingScratch(ctx, oldPath)
	if err != nil {
		return err
	}
	wf.path = newPath
	wf.forceDirty = true  // content unchanged; metadata change alone triggers Put
	wf.unlock = func() {} // lock is held by the caller, not this writeFile
	return wf.Close()
}

// isViewDir reports whether path resolves to a virtual (view-backed) directory
// — one that exists because it has artifact descendants. Files and pending
// directories return false.
func (o *Ops) isViewDir(path string) bool {
	n, err := o.lookupInRoot(path)
	return err == nil && n.FS.IsDir
}

// renamePendingDir re-keys an empty pending directory from oldPath to newPath,
// carrying any nested pending directories with it. The target must be free (no
// file, view-dir, or pending dir). Nothing in the Store or View changes — the
// directory lives only in pendingDirs until a real child lands. The caller
// (Rename) has already checked AllowDirRename.
func (o *Ops) renamePendingDir(oldPath, newPath string) error {
	if err := vfsmeta.ValidatePath(newPath); err != nil {
		return err
	}
	if oldPath == newPath {
		return nil
	}

	unlock := o.pathLocks.LockAll(oldPath, newPath)
	defer unlock()

	if _, err := o.lookupInRoot(newPath); err == nil {
		return fmt.Errorf("%w: %q already exists", errs.ErrPathExists, newPath)
	} else if !errors.Is(err, errs.ErrPathNotFound) {
		return err
	}
	if o.isPendingDir(newPath) {
		return fmt.Errorf("%w: %q is a pending directory", errs.ErrPathExists, newPath)
	}

	o.renamePendingTree(oldPath, newPath)
	return nil
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

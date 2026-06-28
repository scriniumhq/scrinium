package fsops

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
)

// --- Write side ---

// Create makes a new file at path and returns a writable Handle
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
func (o *Ops) Create(ctx context.Context, path string, mode uint32) (Handle, error) {
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

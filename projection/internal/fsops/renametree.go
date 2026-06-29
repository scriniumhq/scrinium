package fsops

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
	"scrinium.dev/projection/pathx"
)

// renameDirTree renames a non-empty (view-backed) directory from oldPath to
// newPath by re-pathing every descendant: one file rename (Put new vfsmeta +
// Delete old + View.Move) per artifact, plus a re-key of any nested pending
// directories. The virtual directories themselves are never moved explicitly —
// they vanish at the old location as their last child leaves and materialise at
// the new location as children arrive.
//
// The whole subtree is locked up front — both paths of every child plus the two
// roots, in one LockAll — so no concurrent writer on those paths can interleave.
// The rename is NOT atomic against readers, and on the first child error it
// aborts, leaving a partially-moved tree; the operation is idempotent over the
// now-smaller source, so the caller can retry.
//
// Content is unchanged, so each child's blob is deduplicated by the Store; only
// the manifest (its vfsmeta.Path) is rewritten.
func (o *Ops) renameDirTree(ctx context.Context, oldPath, newPath string) error {
	if err := vfsmeta.ValidatePath(newPath); err != nil {
		return err
	}
	if oldPath == newPath {
		return nil
	}
	if o.store == nil {
		return fmt.Errorf("projection.Ops.Rename: WithStore not configured")
	}
	// Refuse moving a directory into its own subtree: the destination prefix is
	// itself being moved, which would orphan everything under it.
	if pathx.IsUnder(newPath, oldPath) {
		return fmt.Errorf("%w: cannot rename %q into its own subtree", errs.ErrInvalidPath, newPath)
	}

	// Snapshot the descendant artifacts and their target paths. WalkIn holds the
	// View read lock for the iteration, so collect first, then act.
	type move struct{ from, to string }
	var moves []move
	keys := []string{oldPath, newPath}
	for node, werr := range o.view.WalkIn(o.view.RootView(), oldPath) {
		if werr != nil {
			return werr
		}
		if node.FS.IsDir || node.Artifact == nil {
			continue
		}
		from := node.FS.Path
		to := newPath + from[len(oldPath):]
		moves = append(moves, move{from: from, to: to})
		keys = append(keys, from, to)
	}

	unlock := o.pathLocks.LockAll(keys...)
	defer unlock()

	// Target must be free: no file, view-dir, or pending dir at newPath.
	if o.isViewDir(newPath) || o.isPendingDir(newPath) {
		return fmt.Errorf("%w: %q already exists", errs.ErrPathExists, newPath)
	}
	if _, err := o.lookupInRoot(newPath); err == nil {
		return fmt.Errorf("%w: %q already exists", errs.ErrPathExists, newPath)
	} else if !errors.Is(err, errs.ErrPathNotFound) {
		return err
	}

	for _, m := range moves {
		if err := o.renameArtifactLocked(ctx, m.from, m.to); err != nil {
			return fmt.Errorf("rename %q -> %q: %w", m.from, m.to, err)
		}
	}
	// Carry the old directory's nested pending sub-directories, if any.
	o.renamePendingTree(oldPath, newPath)
	return nil
}

package fsops

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/errs"
	vw "scrinium.dev/projection/internal/view"
)

// RemoveTree removes path and, when path is a directory, everything beneath
// it. This is the WebDAV DELETE contract (RFC 4918 §9.6: a DELETE on a
// collection is depth-infinity), and it is deliberately NOT how Rmdir behaves:
// POSIX rmdir on a non-empty directory must fail, and the FUSE surface relies
// on that, so the recursive form is a separate entry point that only the
// WebDAV adapter calls.
//
// A file deletes exactly as Unlink would. A directory deletes every descendant
// artifact — collected up front from one consistent by-path walk, then removed
// under each artifact's path lock — and drops any pending sub-directories. The
// directories themselves are never deleted explicitly: they are virtual and
// disappear once their last child is gone.
//
// On the first Store/View error the loop aborts and the error is returned,
// leaving the already-deleted entries gone. That mirrors Unlink's own
// partial-failure contract — the DELETE is idempotent over the now-smaller
// tree, so the client can simply retry.
func (o *Ops) RemoveTree(ctx context.Context, path string) error {
	if o.readOnly {
		return fmt.Errorf("%w: RemoveTree on read-only Ops", errs.ErrEditingDisabled)
	}
	if o.store == nil {
		return fmt.Errorf("projection.Ops.RemoveTree: WithStore not configured")
	}

	n, err := o.lookupInRoot(path)
	if err != nil {
		// A pending directory (Mkdir-created, never given a real child) has no
		// tree node but is still removable — drop it and any nested pending.
		if errors.Is(err, errs.ErrPathNotFound) && o.isPendingDir(path) {
			o.dropPendingTree(path)
			return nil
		}
		return err
	}
	if !n.FS.IsDir {
		return o.Unlink(ctx, path)
	}

	// Collect first: WalkIn holds the View read lock for the duration of the
	// iteration, so mutating (Remove) mid-walk would deadlock. Directories are
	// skipped — only artifacts carry an id to delete.
	var files []vw.Node
	for node, werr := range o.view.WalkIn(o.view.RootView(), path) {
		if werr != nil {
			return werr
		}
		if node.FS.IsDir || node.Artifact == nil {
			continue
		}
		files = append(files, node)
	}

	for _, f := range files {
		lock := o.pathLocks.Get(f.FS.Path)
		lock.Lock()
		derr := o.store.Delete(ctx, f.Artifact.ArtifactID)
		if derr == nil {
			derr = o.view.Remove(f.Artifact.ArtifactID)
		}
		lock.Unlock()
		if derr != nil {
			return derr
		}
	}

	o.dropPendingTree(path)
	return nil
}

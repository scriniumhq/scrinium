package localfs

// maintenance.go — filesystem-maintenance operations that are not tied to a
// specific data plane. Currently PruneEmptyDirs (empty-directory cleanup after
// large deletion runs). This is the interface's "Maintenance" group; it is
// deliberately separate from tombstone.go, which owns deletion mechanics, even
// though pruning typically follows a tombstone sweep.

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// PruneEmptyDirs walks the subtree under root and removes empty
// directories bottom-up. Used after large deletion runs (GC Sweep,
// chunk removal) so the directory layout stays tidy.
//
// Idempotent: a tree that is already pruned returns nil.
//
// Safety: the root itself is never removed even if it becomes
// empty; only descendants are eligible. A missing root is not an
// error.
func (d *Driver) PruneEmptyDirs(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := d.resolveDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	// Collect directories bottom-up; we cannot remove during the
	// walk because filepath.WalkDir does not let us re-check
	// emptiness after children have been removed.
	var dirs []string
	walkErr := filepath.WalkDir(full, func(p string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	// Process deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		if dir == full {
			// Never remove the prune root itself.
			continue
		}
		if isDirEmpty(dir) {
			if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// isDirEmpty checks whether a directory has no entries. Errors are
// treated as "not empty" so the caller leaves the directory alone.
func isDirEmpty(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	// Readdirnames(1) returns at most one name; faster than reading
	// the whole listing for the common "empty?" question. io.EOF is
	// the documented success signal on an empty directory.
	names, err := f.Readdirnames(1)
	if err != nil {
		return len(names) == 0 && errors.Is(err, io.EOF)
	}
	return len(names) == 0
}

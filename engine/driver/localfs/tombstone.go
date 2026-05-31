package localfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// MarkTombstone marks a path as logically deleted while preserving
// the original contents on disk for forensics and Recovery.
//
// Behaviour:
//   - If the original file exists, it is renamed to
//     "<path>.tombstone" via atomic rename(2).
//   - If the original file is missing but the tombstone marker
//     does not yet exist, an empty marker file is created. This
//     supports tombstoning a path that was never written, which is
//     a valid signal in multi-host scenarios where the deletion
//     decision arrives before the local replica.
//   - If both files exist (a stray after a crash), the original is
//     removed and the existing marker is kept.
//   - If only the marker exists, the call is a no-op.
//
// The result is idempotent: subsequent MarkTombstone calls on the
// same path return nil without changing the on-disk state.
func (d *Driver) MarkTombstone(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := d.resolve(path)
	if err != nil {
		return err
	}
	tombstone := full + tombstoneSuffix

	srcExists, err := fileExists(full)
	if err != nil {
		return err
	}
	dstExists, err := fileExists(tombstone)
	if err != nil {
		return err
	}

	switch {
	case dstExists && srcExists:
		// Stray after a crash: prefer the existing marker.
		return os.Remove(full)
	case dstExists:
		return nil
	case srcExists:
		return os.Rename(full, tombstone)
	default:
		// Neither exists: create an empty marker so a future
		// IsTombstone reports the deletion intent.
		f, err := os.OpenFile(tombstone,
			os.O_CREATE|os.O_WRONLY|os.O_EXCL,
			d.opts.fileMode,
		)
		if err != nil {
			// A concurrent MarkTombstone race: another caller just
			// created the marker. Treat as success.
			if errors.Is(err, os.ErrExist) {
				return nil
			}
			return err
		}
		return f.Close()
	}
}

// IsTombstone reports whether the given path has a tombstone marker
// on disk. It does NOT check the original file: a missing original
// without a marker is "never existed", not "tombstoned".
func (d *Driver) IsTombstone(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	full, err := d.resolve(path)
	if err != nil {
		return false, err
	}
	return fileExists(full + tombstoneSuffix)
}

// TombstoneInfo reports whether path has a tombstone marker and, if
// so, the marker's modification time — the moment MarkTombstone last
// touched it, which the GC Sweep uses as the start of the grace
// period. A missing marker returns (false, zero, nil).
func (d *Driver) TombstoneInfo(ctx context.Context, path string) (bool, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return false, time.Time{}, err
	}
	full, err := d.resolve(path)
	if err != nil {
		return false, time.Time{}, err
	}
	info, err := os.Lstat(full + tombstoneSuffix)
	if err != nil {
		if os.IsNotExist(err) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	return true, info.ModTime(), nil
}

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

// fileExists is a small helper that returns (true, nil) for a
// regular file or directory, (false, nil) for ENOENT, and the
// underlying error for everything else.
func fileExists(p string) (bool, error) {
	if _, err := os.Lstat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
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

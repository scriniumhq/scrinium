package localfs

import (
	"context"
	"errors"
	"os"
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

// RemoveTombstone deletes the "<path>.tombstone" marker. Keyed by the
// original path so the GC Sweep need not know the suffix. A missing
// marker is a no-op (a Revive may have renamed it back to the original).
func (d *Driver) RemoveTombstone(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := d.resolve(path)
	if err != nil {
		return err
	}
	if err := os.Remove(full + tombstoneSuffix); err != nil && !os.IsNotExist(err) {
		return err
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

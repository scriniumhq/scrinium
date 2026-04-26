package localfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/errs"
)

// tombstoneSuffix is appended to paths whose original entry has been
// tombstoned. See tombstone.go for details.
const tombstoneSuffix = ".tombstone"

// tempPrefix marks files created by createTempFile during an
// in-flight Put or Clone. They are filtered out of List, Stat, and
// iteration; Recover() in M3.4 prunes leftovers from a crashed
// process.
const tempPrefix = "."

// Stat returns metadata about a path. A missing file returns the
// underlying os.ErrNotExist (callable code uses os.IsNotExist /
// errors.Is).
func (d *Driver) Stat(ctx context.Context, path string) (driver.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return driver.FileInfo{}, err
	}
	full, err := d.resolve(path)
	if err != nil {
		return driver.FileInfo{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return driver.FileInfo{}, err
	}
	return driver.FileInfo{
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

// List returns the immediate children of a directory as logical
// (forward-slash, root-relative) paths.
//
// Filtered out:
//   - tombstone marker files (suffix ".tombstone");
//   - in-flight temp files (prefix ".", suffix ".tmp.<hex>").
//
// A non-existent prefix returns os.ErrNotExist; a prefix that is
// not a directory returns the underlying error.
func (d *Driver) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := d.resolveDir(prefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if isHidden(name) {
			continue
		}
		// Convert to a logical path: prefix + "/" + name. Use
		// forward slash because the API contract is OS-independent.
		var logical string
		if prefix == "" || prefix == "." {
			logical = name
		} else {
			logical = strings.TrimRight(prefix, "/") + "/" + name
		}
		out = append(out, logical)
	}
	return out, nil
}

// ListObjectsWithModTime walks the prefix recursively and invokes
// cb for every regular file modified at or after `since`. The
// callback receives ObjectMeta with the file's logical path, size,
// and last-modified time.
//
// Iteration order is the underlying filepath.WalkDir order
// (deterministic on a given filesystem but not formally guaranteed
// across implementations).
//
// To stop iteration early without an error, the callback returns
// errs.ErrStopWalk; the function returns nil to its caller.
//
// Filtered out: tombstones and in-flight temp files (see List).
// Directories are not reported.
func (d *Driver) ListObjectsWithModTime(
	ctx context.Context,
	prefix string,
	since time.Time,
	cb func(driver.ObjectMeta) error,
) error {
	full, err := d.resolveDir(prefix)
	if err != nil {
		// A missing prefix is treated as an empty walk, not an
		// error: the caller often passes a directory that is yet
		// to be created (for instance, during the very first
		// Recovery pass).
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	rootInfo, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !rootInfo.IsDir() {
		// Single-file prefix: treat as one object if it matches.
		if !isHidden(filepath.Base(full)) && !rootInfo.ModTime().Before(since) {
			rel, rerr := d.toLogical(full)
			if rerr != nil {
				return rerr
			}
			return cb(driver.ObjectMeta{
				Path:         rel,
				Size:         rootInfo.Size(),
				LastModified: rootInfo.ModTime(),
			})
		}
		return nil
	}

	walkErr := filepath.WalkDir(full, func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if dirEntry.IsDir() {
			// Skip the entire subtree if the directory itself is
			// hidden (e.g., ".tmp.foo"). Top-level "." prefix is
			// never hidden because we trim it.
			if isHidden(dirEntry.Name()) && path != full {
				return filepath.SkipDir
			}
			return nil
		}
		if isHidden(dirEntry.Name()) {
			return nil
		}
		info, err := dirEntry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(since) {
			return nil
		}
		rel, err := d.toLogical(path)
		if err != nil {
			return err
		}
		if cbErr := cb(driver.ObjectMeta{
			Path:         rel,
			Size:         info.Size(),
			LastModified: info.ModTime(),
		}); cbErr != nil {
			return cbErr
		}
		return nil
	})

	// errs.ErrStopWalk is the documented "stop without error" signal.
	if errors.Is(walkErr, errs.ErrStopWalk) {
		return nil
	}
	return walkErr
}

// CountObjects returns the total number of regular (non-hidden,
// non-tombstone) files under prefix. It walks the subtree but does
// not allocate per-entry data; suitable for capacity stats.
func (d *Driver) CountObjects(ctx context.Context, prefix string) (int64, error) {
	full, err := d.resolveDir(prefix)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var count int64
	walkErr := filepath.WalkDir(full, func(p string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir() {
			if isHidden(e.Name()) && p != full {
				return filepath.SkipDir
			}
			return nil
		}
		if isHidden(e.Name()) {
			return nil
		}
		count++
		return nil
	})
	return count, walkErr
}

// toLogical converts an absolute filesystem path under d.root back
// to the logical (forward-slash, root-relative) form used by the
// driver API.
func (d *Driver) toLogical(absPath string) (string, error) {
	rel, err := filepath.Rel(d.root, absPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// isHidden returns true for filesystem entries the driver hides
// from List/Walk: tombstone markers and in-flight temp files.
//
// Convention:
//   - tombstones end in ".tombstone";
//   - temp files start with "." and contain ".tmp." (createTempFile
//     produces ".<basename>.tmp.<hex>"; using HasPrefix(".") alone
//     is too aggressive — a future directory layout might use
//     dot-prefixed metadata files we DO want to list).
func isHidden(name string) bool {
	if strings.HasSuffix(name, tombstoneSuffix) {
		return true
	}
	if strings.HasPrefix(name, tempPrefix) && strings.Contains(name, ".tmp.") {
		return true
	}
	return false
}

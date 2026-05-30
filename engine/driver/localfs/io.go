package localfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

// Put writes a file atomically. The pattern is:
//  1. Create a sibling temp file ".<basename>.tmp.<random>".
//  2. Stream r into it.
//  3. fsync the temp file (if WithFsync(true), the default).
//  4. Commit it onto the final path:
//     - default: os.Rename. POSIX rename(2) is atomic for observers
//     and unconditionally overwrites any existing target.
//     - WithExclusive(): os.Link, which refuses (EEXIST) to overwrite
//     an existing target — the atomic create-if-absent the engine
//     needs for shared Locations (ADR-26). Maps the conflict to
//     errs.ErrAlreadyExists. The temp and final path briefly share
//     an inode; the temp name is then dropped.
//  5. fsync the parent directory so the commit survives a crash.
//
// A parallel Get either sees the previous contents (or os.ErrNotExist)
// or the new contents — never a partial write. On error the temp file
// is best-effort removed. The final path is not touched until the
// commit succeeds.
func (d *Driver) Put(ctx context.Context, path string, r io.Reader, opts ...driver.PutOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := driver.NewPutConfig(opts...)

	full, err := d.resolve(path)
	if err != nil {
		return err
	}

	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, d.opts.dirMode); err != nil {
		return fmt.Errorf("localfs: mkdir parent: %w", err)
	}

	tmp, err := d.createTempFile(parent, filepath.Base(full))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	// Cleanup on any path that does not commit. After a successful
	// commit the temp name is dropped explicitly (rename moves it;
	// the exclusive link leaves it behind to be removed).
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("localfs: write temp: %w", err)
	}

	if d.opts.fsyncOnWrite {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("localfs: fsync temp: %w", err)
		}
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("localfs: close temp: %w", err)
	}

	if cfg.Exclusive {
		// os.Link is the atomic create-if-absent: it fails with
		// EEXIST rather than clobbering an existing target.
		if err := os.Link(tmpPath, full); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return errs.ErrAlreadyExists
			}
			return fmt.Errorf("localfs: exclusive link: %w", err)
		}
		committed = true
		// The temp now shares an inode with full; drop its name.
		_ = os.Remove(tmpPath)
	} else {
		if err := os.Rename(tmpPath, full); err != nil {
			return fmt.Errorf("localfs: rename: %w", err)
		}
		committed = true
	}

	if d.opts.fsyncOnWrite {
		// Best-effort: a parent fsync makes the rename durable. We
		// log-via-error rather than panic; the caller decides how
		// strict to be in production. fsync on a directory may fail
		// on some filesystems (e.g. tmpfs); treat ENOTSUP as soft.
		if err := fsyncDir(parent); err != nil && !isFsyncDirSoftError(err) {
			return fmt.Errorf("localfs: fsync parent dir: %w", err)
		}
	}

	return nil
}

// createTempFile produces a sibling temp file that the caller is
// responsible for closing/removing. Format:
// ".<basename>.tmp.<8 random hex bytes>".
//
// The ".tmp." suffix is recognised by RebuildIndexAgent (TODO M3.4)
// to prune orphan temp files from a crashed process.
func (d *Driver) createTempFile(dir, base string) (*os.File, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("localfs: rand: %w", err)
	}
	name := fmt.Sprintf(".%s.tmp.%s", base, hex.EncodeToString(nonce[:]))
	return os.OpenFile(filepath.Join(dir, name),
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		d.opts.fileMode,
	)
}

// Get opens a file for streaming reads.
func (d *Driver) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := d.resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// ReadAt opens a file and returns a ReadCloser scoped to the byte
// range [offset, offset+size). Reads past the requested size return
// io.EOF.
//
// The returned reader holds an os.File underneath; closing it
// releases the file descriptor.
func (d *Driver) ReadAt(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("localfs: negative offset or size")
	}
	full, err := d.resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("localfs: seek: %w", err)
	}
	return &limitedFile{r: io.LimitReader(f, size), c: f}, nil
}

// limitedFile is an io.ReadCloser combining an io.LimitedReader
// front with the underlying file's Close.
type limitedFile struct {
	r io.Reader
	c io.Closer
}

func (lf *limitedFile) Read(p []byte) (int, error) { return lf.r.Read(p) }
func (lf *limitedFile) Close() error               { return lf.c.Close() }

// Open implements direct-URI reads for Native locations.
// The localfs driver supports  only the "file://" scheme.
// Other schemes return errs.ErrUnsupportedURIScheme so
// higher layers can fall through to a different driver or
// fail with a clear cause.
//
// The "file://" path is opened directly by absolute filesystem
// path, NOT resolved against the driver root. This is intentional:
// a Native location points outside the managed Store by design.
func (d *Driver) Open(ctx context.Context, uri string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("localfs: parse URI: %w", err)
	}
	if u.Scheme != "file" {
		return nil, errs.ErrUnsupportedURIScheme
	}
	if u.Path == "" {
		return nil, fmt.Errorf("localfs: empty path in file URI")
	}
	return os.Open(u.Path)
}

// Remove deletes a file. Idempotent: removing a non-existent path
// is not an error. This matches the GC Sweep contract — Sweep may
// be retried after a crash and must not fail because a previous
// invocation succeeded.
func (d *Driver) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := d.resolve(path)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Rename moves src to dst atomically. On POSIX rename(2) is atomic;
// observers see either the old name or the new name, never both
// missing. The destination is overwritten if it exists.
//
// The destination's parent directory is created if missing.
func (d *Driver) Rename(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	srcFull, err := d.resolve(src)
	if err != nil {
		return err
	}
	dstFull, err := d.resolve(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstFull), d.opts.dirMode); err != nil {
		return fmt.Errorf("localfs: mkdir dst parent: %w", err)
	}
	return os.Rename(srcFull, dstFull)
}

// Clone copies src to dst with the same atomicity guarantees as
// Put. The destination is written via temp+rename; observers never
// see a partial dst.
//
// CoW optimisations (clonefile on APFS, ioctl FICLONE on
// btrfs/xfs) are deferred. The current implementation always
// streams bytes; correctness over speed for M1.
func (d *Driver) Clone(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	srcFull, err := d.resolve(src)
	if err != nil {
		return err
	}

	in, err := os.Open(srcFull)
	if err != nil {
		return err
	}
	defer in.Close()

	// Reuse the atomic write path of Put.
	return d.Put(ctx, dst, in)
}

// fsyncDir opens the directory and calls fsync on it. Required on
// POSIX after a rename so that the rename survives a crash.
func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// isFsyncDirSoftError treats EINVAL/ENOTSUP from a directory fsync
// as non-fatal. Some filesystems (tmpfs in particular) do not
// support fsync on directories; the rename itself remains correct.
func isFsyncDirSoftError(err error) bool {
	if pathErr, ok := errors.AsType[*os.PathError](err); ok {
		// errno text varies across platforms; match by string for
		// portability instead of importing syscall.
		msg := pathErr.Err.Error()
		return msg == "invalid argument" || msg == "operation not supported"
	}
	return false
}

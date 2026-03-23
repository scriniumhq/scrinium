package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rkurbatov/scrinium/drivers"
)

// fileBlob is a thin wrapper around *os.File to satisfy the drivers.File interface.
// It explicitly adds the Size() method, which is missing from standard os.File.
type fileBlob struct {
	*os.File
}

// Size returns the actual file size from the underlying file system.
func (f *fileBlob) Size() (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// Driver implements drivers.NativeDriver for POSIX-compliant local file systems.
type Driver struct {
	id   string
	root string
}

func NewDriver(id, root string) *Driver {
	return &Driver{
		id:   id,
		root: root,
	}
}

func (d *Driver) ID() string {
	return d.id
}

// Open acquires a non-blocking POSIX flock to ensure exclusive access
// preventing the reading of partially written files.
func (d *Driver) Open(ctx context.Context, path string) (drivers.File, error) {
	fullPath := filepath.Join(d.root, path)
	f, _, ok := openAndStat(ctx, fullPath)
	if !ok {
		return nil, fmt.Errorf("file is locked, missing, or unreadable: %s", fullPath)
	}
	return &fileBlob{File: f}, nil
}

func (d *Driver) Walk(ctx context.Context, prefix string, fn func(drivers.FileInfo) error) error {
	fullPath := filepath.Join(d.root, prefix)
	return filepath.WalkDir(fullPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		return fn(&localFileInfo{FileInfo: info})
	})
}

func (d *Driver) Delete(ctx context.Context, path string) error {
	return os.Remove(filepath.Join(d.root, path))
}

func (d *Driver) Put(ctx context.Context, path string, data io.Reader) error {
	fullPath := filepath.Join(d.root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	out, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, data); err != nil {
		return err
	}
	return out.Sync()
}

func (d *Driver) LinkOrCopy(ctx context.Context, src, dst string) error {
	fullDst := filepath.Join(d.root, dst)
	if err := os.MkdirAll(filepath.Dir(fullDst), 0755); err != nil {
		return err
	}

	// Attempt zero-copy hardlink first.
	if err := os.Link(src, fullDst); err == nil {
		return nil
	}

	// Fallback to buffered copy across partitions.
	return copyFile(ctx, src, fullDst)
}

func (d *Driver) Move(ctx context.Context, src, dst string) error {
	fullDst := filepath.Join(d.root, dst)
	if err := os.MkdirAll(filepath.Dir(fullDst), 0755); err != nil {
		return err
	}
	return moveFile(ctx, src, fullDst)
}

type localFileInfo struct {
	os.FileInfo
}

func (l *localFileInfo) StorageHash() string {
	return "" // LocalFS does not provide O(1) hashing like S3 ETag
}

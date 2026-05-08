package vfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/internal/pathx"
	"github.com/rkurbatov/scrinium/projection"
)

// File is the open-handle interface VFS hands back from
// OpenFile. The contract is the same one webdav.File and
// FUSE expect: Read/Write/Seek/Close + Readdir + Stat. Any
// surface adapter (golang.org/x/net/webdav.File, FUSE node,
// http.ServeContent) consumes File directly.
//
// Read-only files return fs.ErrPermission from Write.
// Directories return fs.ErrInvalid from Read/Write/Seek and
// implement Readdir.
type File interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer

	// Readdir returns the directory entries, with the same
	// semantics as os.File.Readdir: count<=0 returns all,
	// count>0 returns up to that many and io.EOF when
	// exhausted.
	Readdir(count int) ([]os.FileInfo, error)

	// Stat returns this file's FileInfo.
	Stat() (os.FileInfo, error)
}

// --- File implementations ---
//
// Each type is purpose-built; we surface multiple types to
// keep behaviour explicit per case rather than overloading a
// single struct with mode flags.
//
//   - readHandleFile  : read-only over a core.ReadHandle (service
//                       trees, by-X paths inside _scrinium).
//   - bytesFile       : in-memory read-only (stats virtual file).
//   - rwFile          : read/write over projection.File (root view).
//   - blackHoleFile   : write-discarding (used by surface-level
//                       junk-filter wrappers — safe to ignore
//                       when not needed).

// readHandleFile is read-only. Tracks a manual offset to
// satisfy io.Reader/io.Seeker since core.ReadHandle is
// offset-addressable via ReadAt only.
type readHandleFile struct {
	rh    core.ReadHandle
	name  string
	path  string
	size  int64
	mtime time.Time
	isDir bool
	off   int64
}

func (f *readHandleFile) Read(p []byte) (int, error) {
	if !f.rh.SupportsRandomAccess() {
		// Fall back to a streaming read at offset 0 only on
		// the first call; otherwise we'd lose data.
		if f.off != 0 {
			return 0, errors.New("vfs: random access required for non-zero offset")
		}
		n, err := f.rh.Read(p)
		f.off += int64(n)
		return n, err
	}
	n, err := f.rh.ReadAt(p, f.off)
	f.off += int64(n)
	if err == io.EOF && n > 0 {
		// Defer EOF until the next call so the writer sees
		// the last byte.
		err = nil
	}
	return n, err
}

func (f *readHandleFile) Write(p []byte) (int, error) {
	return 0, fs.ErrPermission
}

func (f *readHandleFile) Close() error {
	return f.rh.Close()
}

func (f *readHandleFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		f.off = f.size + offset
	default:
		return 0, fs.ErrInvalid
	}
	if f.off < 0 {
		f.off = 0
		return 0, fs.ErrInvalid
	}
	return f.off, nil
}

func (f *readHandleFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, fs.ErrInvalid // not a directory
}

func (f *readHandleFile) Stat() (os.FileInfo, error) {
	return synthInfo{
		name:    f.name,
		size:    f.size,
		mode:    0o444,
		modTime: f.mtime,
	}, nil
}

// bytesFile is a fully-buffered read-only file backed by a
// byte slice. Used for the stats virtual file.
type bytesFile struct {
	name string
	body []byte
	t    time.Time
	off  int64
}

func newBytesFile(name string, body []byte, t time.Time) *bytesFile {
	return &bytesFile{name: name, body: body, t: t}
}

func (f *bytesFile) Read(p []byte) (int, error) {
	if f.off >= int64(len(f.body)) {
		return 0, io.EOF
	}
	n := copy(p, f.body[f.off:])
	f.off += int64(n)
	return n, nil
}

func (f *bytesFile) Write(p []byte) (int, error) { return 0, fs.ErrPermission }
func (f *bytesFile) Close() error                { return nil }

func (f *bytesFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		f.off = int64(len(f.body)) + offset
	default:
		return 0, fs.ErrInvalid
	}
	if f.off < 0 {
		f.off = 0
		return 0, fs.ErrInvalid
	}
	return f.off, nil
}

func (f *bytesFile) Readdir(count int) ([]os.FileInfo, error) { return nil, fs.ErrInvalid }
func (f *bytesFile) Stat() (os.FileInfo, error) {
	return synthFileInfo(f.name, int64(len(f.body)), f.t), nil
}

// rwFile wraps a projection.File for the root view. Tracks a
// manual offset to satisfy io.Reader/io.Writer/io.Seeker on
// top of the projection.File's WriteAt/ReadAt.
type rwFile struct {
	f      projection.File
	path   string
	size   int64
	mtime  time.Time
	mode   os.FileMode
	isDir  bool
	off    int64
	closed bool
}

func wrapFile(pf projection.File, path string, fi projection.FileInfo) *rwFile {
	return &rwFile{
		f:     pf,
		path:  path,
		size:  fi.Size,
		mtime: fi.ModTime,
		mode:  modeFromUint32(fi.Mode, fi.IsDir),
		isDir: fi.IsDir,
	}
}

// wrapWriteFile is a Create-side variant: a fresh file, size 0.
func wrapWriteFile(pf projection.File, path string) *rwFile {
	return &rwFile{
		f:     pf,
		path:  path,
		size:  0,
		mtime: time.Now().UTC(),
		mode:  0o644,
	}
}

func (f *rwFile) Read(p []byte) (int, error) {
	n, err := f.f.ReadAt(p, f.off)
	f.off += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (f *rwFile) Write(p []byte) (int, error) {
	n, err := f.f.WriteAt(p, f.off)
	f.off += int64(n)
	if f.off > f.size {
		f.size = f.off
	}
	return n, err
}

func (f *rwFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return f.f.Close()
}

func (f *rwFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		f.off = f.size + offset
	default:
		return 0, fs.ErrInvalid
	}
	if f.off < 0 {
		f.off = 0
		return 0, fs.ErrInvalid
	}
	return f.off, nil
}

func (f *rwFile) Readdir(count int) ([]os.FileInfo, error) { return nil, fs.ErrInvalid }
func (f *rwFile) Stat() (os.FileInfo, error) {
	return synthInfo{
		name:    pathx.LastSegment(f.path),
		size:    f.size,
		mode:    f.mode,
		modTime: f.mtime,
	}, nil
}

// blackHoleFile is a write-discarding placeholder. Useful for
// surface-level filters (e.g. WebDAV's OS-junk filter) that
// need to satisfy a client's PUT without actually persisting
// the bytes. Reads return EOF, writes silently succeed,
// nothing reaches the store.
//
// VFS itself doesn't return blackHoleFile from OpenFile —
// surfaces wrap VFS and substitute one when their own policy
// demands.
type blackHoleFile struct {
	name    string
	written int64
	closed  bool
}

// NewBlackHoleFile constructs a write-discarding handle. name
// surfaces in the resulting Stat info.
func NewBlackHoleFile(name string) File {
	return &blackHoleFile{name: name}
}

func (f *blackHoleFile) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (f *blackHoleFile) Write(p []byte) (int, error) {
	f.written += int64(len(p))
	return len(p), nil
}

func (f *blackHoleFile) Close() error {
	f.closed = true
	return nil
}

func (f *blackHoleFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		return offset, nil
	case io.SeekCurrent:
		return f.written, nil
	case io.SeekEnd:
		return f.written + offset, nil
	}
	return 0, fs.ErrInvalid
}

func (f *blackHoleFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, fs.ErrInvalid
}

func (f *blackHoleFile) Stat() (os.FileInfo, error) {
	return synthInfo{
		name:    f.name,
		size:    f.written,
		mode:    0o644,
		modTime: time.Now(),
	}, nil
}

// Compile-time guards.
var (
	_ File = (*readHandleFile)(nil)
	_ File = (*bytesFile)(nil)
	_ File = (*rwFile)(nil)
	_ File = (*blackHoleFile)(nil)
)

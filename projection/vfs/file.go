package vfs

import (
	"errors"
	"io"
	"io/fs"
	"os"

	fso "scrinium.dev/projection/internal/fsops"
	"scrinium.dev/projection/pathx"

	"time"

	"scrinium.dev/domain"
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

// FileAt is a File that additionally supports positioned IO
// (ReadAt/WriteAt). FUSE requires it because the kernel
// addresses file content by offset rather than through a
// streaming cursor. Every regular-file handle implements
// FileAt; directory handles deliberately do not, so
// VFS.OpenFileAt can reject directories with
// errs.ErrIsADirectory.
//
// Per the io.ReaderAt / io.WriterAt contract, ReadAt and
// WriteAt are independent of the Seek cursor and do not
// mutate it.
type FileAt interface {
	File
	io.ReaderAt
	io.WriterAt

	// Sync flushes buffered writes to the backing store.
	// Read-only handles (service trees, stats) are no-ops.
	Sync() error
}

// --- File implementations ---
//
// Each type is purpose-built; we surface multiple types to
// keep behaviour explicit per case rather than overloading a
// single struct with mode flags.
//
//   - readHandleFile  : read-only over a store.ReadHandle (service
//                       trees, by-X paths inside _scrinium).
//   - bytesFile       : in-memory read-only (stats virtual file).
//   - rwFile          : read/write over fso.Handle (root view).
//
// blackHoleFile (write-discarding, surface-only) lives in blackhole.go —
// VFS never returns it from OpenFile.

// calcSeek resolves an io.Seeker request for a cursor-tracking handle. It
// returns the new absolute offset for the given whence; current is the live
// cursor and size the end-of-data offset. A negative result clamps to 0 and
// reports fs.ErrInvalid (the cursor is reset to 0). An unknown whence reports
// fs.ErrInvalid and returns current, leaving the caller's cursor unchanged.
// Shared by the three cursor-backed handles (readHandleFile, bytesFile,
// rwFile); the black-hole handle (blackhole.go) keeps its own stateless seek.
func calcSeek(offset int64, whence int, current, size int64) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = current + offset
	case io.SeekEnd:
		next = size + offset
	default:
		return current, fs.ErrInvalid
	}
	if next < 0 {
		return 0, fs.ErrInvalid
	}
	return next, nil
}

// readHandleFile is read-only. Tracks a manual offset to
// satisfy io.Reader/io.Seeker since store.ReadHandle is
// offset-addressable via ReadAt only.
type readHandleFile struct {
	nonDirStub
	rh    domain.ReadHandle
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
	off, err := calcSeek(offset, whence, f.off, f.size)
	f.off = off
	return off, err
}

func (f *readHandleFile) Stat() (os.FileInfo, error) {
	return synthInfo{
		name:    f.name,
		size:    f.size,
		mode:    0o444,
		modTime: f.mtime,
	}, nil
}

func (f *readHandleFile) ReadAt(p []byte, off int64) (int, error) {
	if !f.rh.SupportsRandomAccess() {
		return 0, errors.New("vfs: read handle does not support random access")
	}
	return f.rh.ReadAt(p, off)
}

func (f *readHandleFile) WriteAt(p []byte, off int64) (int, error) {
	return 0, fs.ErrPermission
}

func (f *readHandleFile) Sync() error { return nil }

// bytesFile is a fully-buffered read-only file backed by a
// byte slice. Used for the stats virtual file.
type bytesFile struct {
	nonDirStub
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
	off, err := calcSeek(offset, whence, f.off, int64(len(f.body)))
	f.off = off
	return off, err
}

func (f *bytesFile) Stat() (os.FileInfo, error) {
	return synthFileInfo(f.name, int64(len(f.body)), f.t), nil
}

func (f *bytesFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	if off >= int64(len(f.body)) {
		return 0, io.EOF
	}
	n := copy(p, f.body[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *bytesFile) WriteAt(p []byte, off int64) (int, error) {
	return 0, fs.ErrPermission
}

func (f *bytesFile) Sync() error { return nil }

// rwFile wraps a fso.Handle for the root view. Tracks a
// manual offset to satisfy io.Reader/io.Writer/io.Seeker on
// top of the fso.Handle's WriteAt/ReadAt.
type rwFile struct {
	nonDirStub
	f      fso.Handle
	path   string
	size   int64
	mtime  time.Time
	mode   os.FileMode
	isDir  bool
	off    int64
	closed bool
}

func wrapFile(pf fso.Handle, path string, fi fso.FileInfo) *rwFile {
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
func wrapWriteFile(pf fso.Handle, path string) *rwFile {
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
	off, err := calcSeek(offset, whence, f.off, f.size)
	f.off = off
	return off, err
}

func (f *rwFile) Stat() (os.FileInfo, error) {
	return synthInfo{
		name:    pathx.LastSegment(f.path),
		size:    f.size,
		mode:    f.mode,
		modTime: f.mtime,
	}, nil
}

func (f *rwFile) ReadAt(p []byte, off int64) (int, error) {
	return f.f.ReadAt(p, off)
}

// WriteAt assumes a single writer per handle (the FUSE/WebDAV
// convention): it updates the cached size without locking,
// matching the existing Write path.
func (f *rwFile) WriteAt(p []byte, off int64) (int, error) {
	n, err := f.f.WriteAt(p, off)
	if end := off + int64(n); end > f.size {
		f.size = end
	}
	return n, err
}

func (f *rwFile) Sync() error { return f.f.Sync() }

// Compile-time guards. blackHoleFile's guards live in blackhole.go.
var (
	_ File = (*readHandleFile)(nil)
	_ File = (*bytesFile)(nil)
	_ File = (*rwFile)(nil)

	_ FileAt = (*readHandleFile)(nil)
	_ FileAt = (*bytesFile)(nil)
	_ FileAt = (*rwFile)(nil)
)

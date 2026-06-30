package vfs

import (
	"io"
	"io/fs"
	"os"
	"time"
)

// blackHoleFile is a write-discarding placeholder. Useful for
// surface-level filters (e.g. WebDAV's OS-junk filter) that
// need to satisfy a client's PUT without actually persisting
// the bytes. Reads return EOF, writes silently succeed,
// nothing reaches the store.
//
// It is deliberately kept out of file.go: VFS never returns a
// blackHoleFile from OpenFile. Surfaces wrap VFS and substitute one
// (via NewBlackHoleFile) when their own policy demands — the only
// caller today is the WebDAV junk filter in cmd/scrinium-webdav.
//
// Its Seek is intentionally stateless and does not go through calcSeek:
// with no real cursor, SeekStart echoes the offset and SeekCurrent
// reports the running written count.
type blackHoleFile struct {
	nonDirStub
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

func (f *blackHoleFile) Stat() (os.FileInfo, error) {
	return synthInfo{
		name:    f.name,
		size:    f.written,
		mode:    0o644,
		modTime: time.Now(),
	}, nil
}

func (f *blackHoleFile) ReadAt(p []byte, off int64) (int, error) {
	return 0, io.EOF
}

func (f *blackHoleFile) WriteAt(p []byte, off int64) (int, error) {
	f.written += int64(len(p))
	return len(p), nil
}

func (f *blackHoleFile) Sync() error { return nil }

// Compile-time guards.
var (
	_ File   = (*blackHoleFile)(nil)
	_ FileAt = (*blackHoleFile)(nil)
)

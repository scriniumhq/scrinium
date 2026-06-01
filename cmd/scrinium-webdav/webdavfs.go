package main

import (
	"context"
	"io/fs"
	"os"
	fso "scrinium.dev/projection/fsops"
	vw "scrinium.dev/projection/view"
	"strings"
	"syscall"

	"golang.org/x/net/webdav"
	"scrinium.dev/internal/pathx"
	"scrinium.dev/projection/vfs"
)

// webdavFS adapts vfs.VFS to webdav.FileSystem. Almost all
// the work — routing, file types, error wrapping — lives in
// vfs/. This adapter contributes WebDAV-specific behaviour
// only:
//
//   - the cleanWebDAVPath transformation (drop leading
//     slash);
//   - the OS-junk filter (Finder/Office sidecar files);
//   - the black-hole substitution for junk PUTs.
type webdavFS struct {
	v          *vfs.VFS
	rejectJunk bool
}

func newWebdavFS(
	view *vw.View,
	fsops *fso.Ops,
	cfg vfs.Config,
	rejectJunk bool,
	statsProvider func() []byte,
) *webdavFS {
	opts := []vfs.Option{}
	if statsProvider != nil {
		opts = append(opts, vfs.WithStatsProvider(statsProvider))
	}
	if rejectJunk {
		// VFS-level filter suppresses junk from listings.
		// Stat/OpenFile junk handling needs WebDAV-specific
		// black-hole semantics, which we do here.
		opts = append(opts, vfs.WithNameFilter(isOSJunk))
	}
	return &webdavFS{
		v:          vfs.New(view, fsops, cfg, opts...),
		rejectJunk: rejectJunk,
	}
}

// VFS returns the underlying vfs.VFS. The web handler uses
// it directly (read paths only) when wired alongside WebDAV.
func (w *webdavFS) VFS() *vfs.VFS { return w.v }

// --- webdav.FileSystem ---

func (w *webdavFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	clean := cleanWebDAVPath(name)
	if w.rejectJunk && isOSJunk(clean) {
		return fs.ErrPermission
	}
	return w.v.Mkdir(ctx, clean, perm)
}

func (w *webdavFS) RemoveAll(ctx context.Context, name string) error {
	clean := cleanWebDAVPath(name)
	return w.v.RemoveAll(ctx, clean)
}

func (w *webdavFS) Rename(ctx context.Context, oldName, newName string) error {
	oldClean := cleanWebDAVPath(oldName)
	newClean := cleanWebDAVPath(newName)
	if w.rejectJunk && isOSJunk(newClean) {
		return fs.ErrPermission
	}
	return w.v.Rename(ctx, oldClean, newClean)
}

func (w *webdavFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	clean := cleanWebDAVPath(name)
	if w.rejectJunk && isOSJunk(clean) {
		return nil, fs.ErrNotExist
	}
	return w.v.Stat(ctx, clean)
}

func (w *webdavFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	clean := cleanWebDAVPath(name)
	if w.rejectJunk && isOSJunk(clean) {
		// Junk handling is Finder-friendly: a hard 403 on
		// PUT breaks macOS's two-step AppleDouble copy (it
		// sends PUT /._<name> first; if that fails it
		// ABORTS the real PUT). Present a black-hole writer
		// for create/write paths — Finder gets 200 OK, the
		// bytes land in /dev/null, the store stays clean.
		// Reads remain ENOENT so listing/stat stays honest.
		if flag&(syscall.O_CREAT|os.O_WRONLY|os.O_RDWR) != 0 {
			return webdavFileAdapter{vfs.NewBlackHoleFile(pathx.LastSegment(clean))}, nil
		}
		return nil, fs.ErrNotExist
	}
	f, err := w.v.OpenFile(ctx, clean, flag, perm)
	if err != nil {
		return nil, err
	}
	return webdavFileAdapter{f}, nil
}

// webdavFileAdapter wraps a vfs.File as a webdav.File. The
// two interfaces are structurally identical (Read/Write/
// Seek/Close + Readdir + Stat); the wrapper exists only to
// satisfy Go's interface declaration: webdav.File is a
// distinct named type, so a vfs.File doesn't auto-satisfy
// it without a wrapper.
type webdavFileAdapter struct{ f vfs.File }

func (a webdavFileAdapter) Read(p []byte) (int, error)  { return a.f.Read(p) }
func (a webdavFileAdapter) Write(p []byte) (int, error) { return a.f.Write(p) }
func (a webdavFileAdapter) Close() error                { return a.f.Close() }
func (a webdavFileAdapter) Seek(off int64, whence int) (int64, error) {
	return a.f.Seek(off, whence)
}
func (a webdavFileAdapter) Readdir(count int) ([]os.FileInfo, error) {
	return a.f.Readdir(count)
}
func (a webdavFileAdapter) Stat() (os.FileInfo, error) { return a.f.Stat() }

// --- helpers ---

// cleanWebDAVPath strips the leading slash that WebDAV
// always sends and any trailing slash on collection paths.
func cleanWebDAVPath(name string) string {
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimSuffix(name, "/")
	return name
}

// Compile-time guard.
var _ webdav.FileSystem = (*webdavFS)(nil)
var _ webdav.File = webdavFileAdapter{}

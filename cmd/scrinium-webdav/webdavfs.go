package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/projection"
)

// POSIX flag bits used for OpenFile semantics. Aliased to
// syscall constants for the platforms we support; the values are
// stable on Linux/macOS/Windows for these particular flags.
const (
	syscallOCreate = syscall.O_CREAT
	syscallOExcl   = syscall.O_EXCL
)

// webdavFS adapts projection.FSOps + projection.View to
// webdav.FileSystem. It is the WebDAV equivalent of FUSE's
// node tree: every method translates a WebDAV-shaped path into
// a Route classification, then delegates to FSOps (root view) or
// View (service trees) accordingly.
//
// Concurrency: webdav.Handler may dispatch concurrent requests on
// different files; the wrapped File implementations carry their
// own locking. This adapter itself is stateless beyond the
// configuration captured at construction.
type webdavFS struct {
	view       *projection.View
	fsops      *projection.FSOps
	routingCfg projection.RoutingConfig
	startedAt  time.Time
	rejectJunk bool
}

func newWebdavFS(view *projection.View, fsops *projection.FSOps, cfg projection.RoutingConfig, rejectJunk bool) *webdavFS {
	return &webdavFS{
		view:       view,
		fsops:      fsops,
		routingCfg: cfg,
		startedAt:  time.Now().UTC(),
		rejectJunk: rejectJunk,
	}
}

// --- webdav.FileSystem ---

func (w *webdavFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	clean := cleanWebDAVPath(name)
	if w.rejectJunk && isOSJunk(clean) {
		return fs.ErrPermission
	}
	if isAtServiceRoot(clean, w.routingCfg) {
		return wrapErrno(errs.ErrEditingDisabled)
	}
	return wrapErr(w.fsops.Mkdir(clean, uint32(perm)))
}

func (w *webdavFS) RemoveAll(ctx context.Context, name string) error {
	clean := cleanWebDAVPath(name)
	if clean == "" {
		// Refuse to remove the mount root.
		return wrapErrno(errs.ErrEditingDisabled)
	}
	if isAtServiceRoot(clean, w.routingCfg) {
		return wrapErrno(errs.ErrEditingDisabled)
	}
	// First try Unlink (file). If it says ErrIsADirectory, fall
	// back to Rmdir. WebDAV's RemoveAll is recursive in spirit,
	// but our model rejects directory trees with content
	// (ErrNotEmpty). Recursive deletion of non-empty service
	// trees is intentionally not implemented.
	err := w.fsops.Unlink(ctx, clean)
	if err == nil {
		return nil
	}
	if errors.Is(err, errs.ErrIsADirectory) {
		return wrapErr(w.fsops.Rmdir(clean))
	}
	return wrapErr(err)
}

func (w *webdavFS) Rename(ctx context.Context, oldName, newName string) error {
	oldClean := cleanWebDAVPath(oldName)
	newClean := cleanWebDAVPath(newName)
	if w.rejectJunk && isOSJunk(newClean) {
		return fs.ErrPermission
	}
	if isAtServiceRoot(newClean, w.routingCfg) {
		return wrapErrno(errs.ErrEditingDisabled)
	}
	return wrapErr(w.fsops.Rename(ctx, oldClean, newClean))
}

func (w *webdavFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	clean := cleanWebDAVPath(name)
	if w.rejectJunk && isOSJunk(clean) {
		return nil, fs.ErrNotExist
	}
	target, err := projection.Route(clean, w.routingCfg)
	if err != nil {
		if errors.Is(err, projection.ErrRouteRejected) {
			return nil, fs.ErrNotExist
		}
		return nil, wrapErr(err)
	}
	switch target.Kind {
	case projection.RouteRoot:
		fi, err := w.fsops.Stat(target.SubPath)
		if err != nil {
			return nil, wrapErr(err)
		}
		return projectionFileInfo{fi: fi}, nil
	case projection.RouteServiceRoot:
		return synthDirInfo(w.routingCfg.ServicePrefix, w.startedAt), nil
	case projection.RouteServiceTree:
		node, err := serviceLookup(w.view, target.Tree, target.SubPath)
		if err != nil {
			return nil, wrapErr(err)
		}
		return projectionNodeInfo{node: node, fallbackTime: w.startedAt}, nil
	case projection.RouteStatsFile:
		body := w.statsBody()
		return synthFileInfo("stats", int64(len(body)), time.Now()), nil
	case projection.RouteRawMirror:
		return nil, fs.ErrNotExist // 5c
	}
	return nil, fs.ErrNotExist
}

func (w *webdavFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	clean := cleanWebDAVPath(name)
	if w.rejectJunk && isOSJunk(clean) {
		// Junk handling is Finder-friendly: a hard 403 on PUT
		// breaks macOS's two-step AppleDouble copy (it sends
		// PUT /._<name> first; if that fails it ABORTS the
		// real PUT). So we present a black-hole writer for
		// create/write paths — Finder gets 200 OK, the bytes
		// land in /dev/null, the store stays clean. Reads
		// remain ENOENT so the listing/stat surface stays
		// honest.
		if flag&(syscallOCreate|os.O_WRONLY|os.O_RDWR) != 0 {
			return newBlackHoleFile(lastSegment(clean)), nil
		}
		return nil, fs.ErrNotExist
	}
	target, err := projection.Route(clean, w.routingCfg)
	if err != nil {
		if errors.Is(err, projection.ErrRouteRejected) {
			return nil, fs.ErrNotExist
		}
		return nil, wrapErr(err)
	}
	// Create requested?
	wantCreate := flag&syscallOCreate != 0
	wantTrunc := flag&os.O_TRUNC != 0
	wantWrite := flag&(os.O_WRONLY|os.O_RDWR) != 0
	_ = wantTrunc

	switch target.Kind {
	case projection.RouteRoot:
		return w.openRoot(ctx, target.SubPath, flag, perm, wantCreate, wantWrite)

	case projection.RouteServiceRoot:
		// Directory-like; must have read-only flags.
		if wantWrite || wantCreate {
			return nil, wrapErrno(errs.ErrEditingDisabled)
		}
		return newServiceDirFile(w, w.routingCfg.ServicePrefix, "", true), nil

	case projection.RouteServiceTree:
		if wantWrite || wantCreate {
			return nil, wrapErrno(errs.ErrEditingDisabled)
		}
		node, err := serviceLookup(w.view, target.Tree, target.SubPath)
		if err != nil {
			return nil, wrapErr(err)
		}
		if node.FS.IsDir {
			return newServiceDirFile(w, target.Tree, target.SubPath, false), nil
		}
		rh, err := serviceOpen(ctx, w.view, target.Tree, target.SubPath)
		if err != nil {
			return nil, wrapErr(err)
		}
		return &readHandleFile{
			rh:    rh,
			name:  lastSegmentOf(target.SubPath),
			path:  target.SubPath,
			size:  node.FS.Size,
			mtime: nodeModTime(node, w.startedAt),
			isDir: false,
		}, nil

	case projection.RouteStatsFile:
		if wantWrite || wantCreate {
			return nil, wrapErrno(errs.ErrEditingDisabled)
		}
		body := w.statsBody()
		return newBytesFile("stats", body, time.Now()), nil

	case projection.RouteRawMirror:
		return nil, fs.ErrNotExist // 5c
	}
	return nil, fs.ErrNotExist
}

// openRoot is the FSOps-backed side of OpenFile.
func (w *webdavFS) openRoot(
	ctx context.Context,
	subPath string,
	flag int,
	perm os.FileMode,
	wantCreate, wantWrite bool,
) (webdav.File, error) {
	// Empty subPath = mount root; always treat as readable
	// directory.
	if subPath == "" {
		return newRootDirFile(w), nil
	}
	if wantCreate {
		// O_CREAT semantics: try to create; on EEXIST fall through
		// to open if O_EXCL was not set.
		f, err := w.fsops.Create(ctx, subPath, uint32(perm))
		if err == nil {
			return wrapWriteFile(f, subPath), nil
		}
		if !errors.Is(err, errs.ErrPathExists) || flag&syscallOExcl != 0 {
			return nil, wrapErr(err)
		}
		// EEXIST without O_EXCL — fall through to open below.
	}
	// Stat first to decide file vs dir.
	fi, err := w.fsops.Stat(subPath)
	if err != nil {
		return nil, wrapErr(err)
	}
	if fi.IsDir {
		// Read-only dir handle for Readdir.
		return newPathDirFile(w, subPath), nil
	}
	mode := projection.OpenReadOnly
	if wantWrite {
		mode = projection.OpenReadWrite
	}
	f, err := w.fsops.Open(ctx, subPath, mode)
	if err != nil {
		return nil, wrapErr(err)
	}
	return wrapFile(f, subPath, fi), nil
}

// statsBody renders the current ViewStats. Mirrors the FUSE side
// for parity.
func (w *webdavFS) statsBody() []byte {
	stats := w.view.Stats
	var b strings.Builder
	fmt.Fprintf(&b, "Scrinium projection stats\n")
	fmt.Fprintf(&b, "Source:          %s\n", w.view.Source)
	fmt.Fprintf(&b, "Started:         %s\n", w.startedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "TotalNodes:      %d\n", stats.TotalNodes)
	fmt.Fprintf(&b, "TotalBytes:      %d\n", stats.TotalBytes)
	fmt.Fprintf(&b, "OrphanedCount:   %d\n", stats.OrphanedCount)
	fmt.Fprintf(&b, "CollisionCount:  %d\n", stats.CollisionCount)
	fmt.Fprintf(&b, "SessionCount:    %d\n", stats.SessionCount)
	fmt.Fprintf(&b, "NamespaceCount:  %d\n", stats.NamespaceCount)
	return []byte(b.String())
}

// --- Helpers ---

// cleanWebDAVPath strips the leading slash that WebDAV always
// includes ("/photos/img.jpg" → "photos/img.jpg") plus any
// trailing slash.
func cleanWebDAVPath(name string) string {
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimSuffix(name, "/")
	return name
}

// isAtServiceRoot reports whether a path is exactly the service
// prefix or anything under it. Service tree paths are read-only;
// mutating methods reject them.
func isAtServiceRoot(clean string, cfg projection.RoutingConfig) bool {
	if cfg.ServicePrefix == "" {
		return false
	}
	return clean == cfg.ServicePrefix ||
		strings.HasPrefix(clean, cfg.ServicePrefix+"/")
}

// nodeModTime returns the mtime to surface for a service-tree
// Node, falling back to the daemon start time when the node has
// no mtime of its own (typical for synthetic virtual directories).
func nodeModTime(n projection.Node, fallback time.Time) time.Time {
	if !n.FS.ModTime.IsZero() {
		return n.FS.ModTime
	}
	return fallback
}

func lastSegmentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

// --- Error mapping ---

// wrapErr translates a projection error into the standard fs.*
// errors that webdav.Handler maps to HTTP status codes
// (404/403/409/etc.). Returns nil unchanged.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errs.ErrPathNotFound):
		return fs.ErrNotExist
	case errors.Is(err, errs.ErrPathExists):
		return fs.ErrExist
	case errors.Is(err, errs.ErrIsADirectory),
		errors.Is(err, errs.ErrNotADirectory),
		errors.Is(err, errs.ErrNotEmpty),
		errors.Is(err, errs.ErrInvalidPath):
		return fs.ErrInvalid
	case errors.Is(err, errs.ErrEditingDisabled),
		errors.Is(err, errs.ErrScratchQuota):
		return fs.ErrPermission
	}
	return err
}

// wrapErrno produces an fs.*-class error directly from a
// projection sentinel without wrapping in a fmt.Errorf.
func wrapErrno(err error) error {
	switch {
	case errors.Is(err, errs.ErrPathNotFound):
		return fs.ErrNotExist
	case errors.Is(err, errs.ErrPathExists):
		return fs.ErrExist
	case errors.Is(err, errs.ErrEditingDisabled):
		return fs.ErrPermission
	}
	return err
}

// --- service helpers ---

// serviceLookup dispatches View.GetByX for service-tree access.
func serviceLookup(view *projection.View, tree projection.RootView, sub string) (projection.Node, error) {
	switch tree {
	case projection.RootByPath:
		return view.GetByPath(sub)
	case projection.RootBySession:
		return view.GetBySession(sub)
	case projection.RootByNamespace:
		return view.GetByNamespace(sub)
	case projection.RootByDate:
		return view.GetByDate(sub)
	case projection.RootByArtifact:
		return view.GetByArtifact(sub)
	}
	return projection.Node{}, errs.ErrPathNotFound
}

func serviceList(view *projection.View, tree projection.RootView, sub string) projection.NodeSeq {
	switch tree {
	case projection.RootByPath:
		return view.ListByPath(sub)
	case projection.RootBySession:
		return view.ListBySession(sub)
	case projection.RootByNamespace:
		return view.ListByNamespace(sub)
	case projection.RootByDate:
		return view.ListByDate(sub)
	case projection.RootByArtifact:
		return view.ListByArtifact(sub)
	}
	return func(yield func(projection.Node, error) bool) {
		yield(projection.Node{}, errs.ErrPathNotFound)
	}
}

func serviceOpen(ctx context.Context, view *projection.View, tree projection.RootView, sub string) (core.ReadHandle, error) {
	switch tree {
	case projection.RootByPath:
		return view.OpenByPath(ctx, sub, domain.GetOptions{})
	case projection.RootBySession:
		return view.OpenBySession(ctx, sub, domain.GetOptions{})
	case projection.RootByNamespace:
		return view.OpenByNamespace(ctx, sub, domain.GetOptions{})
	case projection.RootByDate:
		return view.OpenByDate(ctx, sub, domain.GetOptions{})
	case projection.RootByArtifact:
		return view.OpenByArtifact(ctx, sub, domain.GetOptions{})
	}
	return nil, errs.ErrPathNotFound
}

// --- os.FileInfo adapters ---

// projectionFileInfo wraps a projection.FileInfo as os.FileInfo.
type projectionFileInfo struct {
	fi projection.FileInfo
}

func (p projectionFileInfo) Name() string       { return p.fi.Name }
func (p projectionFileInfo) Size() int64        { return p.fi.Size }
func (p projectionFileInfo) Mode() os.FileMode  { return modeFromUint32(p.fi.Mode, p.fi.IsDir) }
func (p projectionFileInfo) ModTime() time.Time { return p.fi.ModTime }
func (p projectionFileInfo) IsDir() bool        { return p.fi.IsDir }
func (p projectionFileInfo) Sys() any           { return nil }

// projectionNodeInfo wraps a projection.Node as os.FileInfo for
// the service-tree side. POSIX attributes are best-effort: the
// service trees do not run through FSOps so fsmeta is not
// decoded — we surface 0o555 for dirs and the inferred default
// for files.
type projectionNodeInfo struct {
	node         projection.Node
	fallbackTime time.Time
}

func (p projectionNodeInfo) Name() string { return p.node.FS.Name }
func (p projectionNodeInfo) Size() int64  { return p.node.FS.Size }
func (p projectionNodeInfo) Mode() os.FileMode {
	if p.node.FS.IsDir {
		return os.ModeDir | 0o555
	}
	return 0o444
}
func (p projectionNodeInfo) ModTime() time.Time {
	return nodeModTime(p.node, p.fallbackTime)
}
func (p projectionNodeInfo) IsDir() bool { return p.node.FS.IsDir }
func (p projectionNodeInfo) Sys() any    { return nil }

// synthDirInfo / synthFileInfo are quick os.FileInfo for
// virtual directories (service prefix root) and virtual files
// (stats).
type synthInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (s synthInfo) Name() string       { return s.name }
func (s synthInfo) Size() int64        { return s.size }
func (s synthInfo) Mode() os.FileMode  { return s.mode }
func (s synthInfo) ModTime() time.Time { return s.modTime }
func (s synthInfo) IsDir() bool        { return s.isDir }
func (s synthInfo) Sys() any           { return nil }

func synthDirInfo(name string, t time.Time) os.FileInfo {
	return synthInfo{name: name, mode: os.ModeDir | 0o555, modTime: t, isDir: true}
}

func synthFileInfo(name string, size int64, t time.Time) os.FileInfo {
	return synthInfo{name: name, size: size, mode: 0o444, modTime: t}
}

// modeFromUint32 produces os.FileMode from a POSIX mode plus an
// IsDir flag. Mirrors os.FileInfo conventions.
func modeFromUint32(m uint32, isDir bool) os.FileMode {
	mode := os.FileMode(m & 0o7777)
	if isDir {
		mode |= os.ModeDir
	}
	return mode
}

// --- File handle types ---
//
// webdav.File requires Read/Write/Seek/Close + Readdir + Stat.
// We surface multiple types to keep behaviour explicit per case:
//
//   - readHandleFile  : read-only over a core.ReadHandle (service
//                       trees, by-X paths inside _scrinium).
//   - bytesFile       : in-memory read-only (stats virtual file).
//   - rwFile          : read/write over projection.File (root view).
//   - dirFile         : directory listing — delegates to FSOps or
//                       View.

// readHandleFile is read-only. Tracks a manual offset to satisfy
// io.Reader/io.Seeker since core.ReadHandle is offset-addressable
// via ReadAt only.
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
		// Fall back to a streaming read at offset 0 only on the
		// first call; otherwise we'd lose data.
		if f.off != 0 {
			return 0, errors.New("webdav: random access required for non-zero offset")
		}
		n, err := f.rh.Read(p)
		f.off += int64(n)
		return n, err
	}
	n, err := f.rh.ReadAt(p, f.off)
	f.off += int64(n)
	if err == io.EOF && n > 0 {
		// Defer EOF until the next call so the writer sees the
		// last byte.
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

// bytesFile is a fully-buffered read-only file backed by a byte
// slice. Used for the stats virtual file.
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
// manual offset to satisfy io.Reader/io.Writer/io.Seeker on top
// of the projection.File's WriteAt/ReadAt.
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
		name:    lastSegmentOf(f.path),
		size:    f.size,
		mode:    f.mode,
		modTime: f.mtime,
	}, nil
}

// --- Directory file handles ---

// rootDirFile listings come from FSOps.Listdir(""). The service
// prefix entry is appended.
type rootDirFile struct {
	w        *webdavFS
	consumed bool
}

func newRootDirFile(w *webdavFS) *rootDirFile { return &rootDirFile{w: w} }

func (d *rootDirFile) Read(p []byte) (int, error)  { return 0, fs.ErrInvalid }
func (d *rootDirFile) Write(p []byte) (int, error) { return 0, fs.ErrPermission }
func (d *rootDirFile) Close() error                { return nil }
func (d *rootDirFile) Seek(int64, int) (int64, error) {
	return 0, fs.ErrInvalid
}

func (d *rootDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.consumed {
		return nil, io.EOF
	}
	d.consumed = true
	var out []os.FileInfo
	for fi, err := range d.w.fsops.Listdir("") {
		if err != nil {
			return nil, wrapErr(err)
		}
		if d.w.rejectJunk && isOSJunk(fi.Name) {
			continue
		}
		out = append(out, projectionFileInfo{fi: fi})
	}
	if d.w.routingCfg.ServicePrefix != "" {
		out = append(out, synthDirInfo(d.w.routingCfg.ServicePrefix, d.w.startedAt))
	}
	return out, nil
}

func (d *rootDirFile) Stat() (os.FileInfo, error) {
	return synthDirInfo("/", d.w.startedAt), nil
}

// pathDirFile is a directory inside the root view. Delegates
// listing to FSOps.
type pathDirFile struct {
	w        *webdavFS
	subPath  string
	consumed bool
}

func newPathDirFile(w *webdavFS, subPath string) *pathDirFile {
	return &pathDirFile{w: w, subPath: subPath}
}

func (d *pathDirFile) Read(p []byte) (int, error)     { return 0, fs.ErrInvalid }
func (d *pathDirFile) Write(p []byte) (int, error)    { return 0, fs.ErrPermission }
func (d *pathDirFile) Close() error                   { return nil }
func (d *pathDirFile) Seek(int64, int) (int64, error) { return 0, fs.ErrInvalid }

func (d *pathDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.consumed {
		return nil, io.EOF
	}
	d.consumed = true
	var out []os.FileInfo
	for fi, err := range d.w.fsops.Listdir(d.subPath) {
		if err != nil {
			return nil, wrapErr(err)
		}
		if d.w.rejectJunk && isOSJunk(fi.Name) {
			continue
		}
		out = append(out, projectionFileInfo{fi: fi})
	}
	return out, nil
}

func (d *pathDirFile) Stat() (os.FileInfo, error) {
	fi, err := d.w.fsops.Stat(d.subPath)
	if err != nil {
		return nil, wrapErr(err)
	}
	return projectionFileInfo{fi: fi}, nil
}

// serviceDirFile lists service-tree directories or the service
// prefix root. Two modes:
//
//   - serviceRoot: the prefix dir itself (lists trees + stats).
//   - serviceTree: a directory inside a tree (lists View nodes).
type serviceDirFile struct {
	w        *webdavFS
	tree     projection.RootView
	subPath  string
	isPrefix bool
	consumed bool
}

func newServiceDirFile(w *webdavFS, treeOrPrefix any, subPath string, isPrefix bool) *serviceDirFile {
	d := &serviceDirFile{
		w:        w,
		subPath:  subPath,
		isPrefix: isPrefix,
	}
	if !isPrefix {
		if t, ok := treeOrPrefix.(projection.RootView); ok {
			d.tree = t
		}
	}
	return d
}

func (d *serviceDirFile) Read(p []byte) (int, error)     { return 0, fs.ErrInvalid }
func (d *serviceDirFile) Write(p []byte) (int, error)    { return 0, fs.ErrPermission }
func (d *serviceDirFile) Close() error                   { return nil }
func (d *serviceDirFile) Seek(int64, int) (int64, error) { return 0, fs.ErrInvalid }

func (d *serviceDirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.consumed {
		return nil, io.EOF
	}
	d.consumed = true
	if d.isPrefix {
		// List enabled service trees + stats.
		cfg := d.w.routingCfg
		var out []os.FileInfo
		add := func(name string) {
			out = append(out, synthDirInfo(name, d.w.startedAt))
		}
		if cfg.ShowStats {
			out = append(out, synthFileInfo("stats", int64(len(d.w.statsBody())), time.Now()))
		}
		if cfg.ShowByArtifact {
			add("by-artifact")
		}
		if cfg.ShowByDate {
			add("by-date")
		}
		if cfg.ShowBySession {
			add("by-session")
		}
		if cfg.ShowByNamespace {
			add("by-namespace")
		}
		if cfg.ShowOrphaned {
			add("orphaned")
		}
		if cfg.ShowRaw {
			add("raw")
		}
		add("by-path")
		return out, nil
	}
	// Service-tree listing.
	var out []os.FileInfo
	for n, err := range serviceList(d.w.view, d.tree, d.subPath) {
		if err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, projectionNodeInfo{node: n, fallbackTime: d.w.startedAt})
	}
	return out, nil
}

func (d *serviceDirFile) Stat() (os.FileInfo, error) {
	if d.isPrefix {
		return synthDirInfo(d.w.routingCfg.ServicePrefix, d.w.startedAt), nil
	}
	node, err := serviceLookup(d.w.view, d.tree, d.subPath)
	if err != nil {
		return nil, wrapErr(err)
	}
	return projectionNodeInfo{node: node, fallbackTime: d.w.startedAt}, nil
}

// blackHoleFile is a write-accepting, read-empty webdav.File used
// to absorb OS junk PUTs (AppleDouble ._<name>, etc.) without
// committing anything to the store. Writes succeed, the byte
// count is reported back, but no payload reaches the View.
//
// This exists because macOS Finder's copy protocol PUTs an
// AppleDouble sidecar BEFORE the real file; rejecting that
// sidecar with 403 causes Finder to abort the whole copy
// (visible to the user as Error -43). Accepting it as a black
// hole keeps Finder's state machine happy while the store stays
// junk-free.
type blackHoleFile struct {
	name    string
	written int64
	closed  bool
}

func newBlackHoleFile(name string) *blackHoleFile {
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
	_ webdav.FileSystem = (*webdavFS)(nil)
	_ webdav.File       = (*readHandleFile)(nil)
	_ webdav.File       = (*bytesFile)(nil)
	_ webdav.File       = (*rwFile)(nil)
	_ webdav.File       = (*rootDirFile)(nil)
	_ webdav.File       = (*pathDirFile)(nil)
	_ webdav.File       = (*serviceDirFile)(nil)
	_ webdav.File       = (*blackHoleFile)(nil)
)

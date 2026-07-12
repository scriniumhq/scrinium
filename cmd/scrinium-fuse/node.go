//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"log/slog"
	"os"
	"scrinium.dev/domain"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"scrinium.dev/errs"
	"scrinium.dev/internal/slogx"
	"scrinium.dev/projection/pathx"
	"scrinium.dev/projection/vfs"
)

// fuseFS is the shared state every node points at: the VFS facade
// plus the boot timestamp used for the synthetic mount-root attrs.
// Routing, service trees, the stats file, and read-only enforcement
// all live behind the facade — nodes never touch the projection
// primitive directly.
type fuseFS struct {
	v         *vfs.VFS
	startedAt time.Time
	log       *slog.Logger
	// mode reports the store's maintenance mode; every read op that
	// draws from the projection past the store gates consults it
	// per-request (ADR-111, INV-111-5). Nil = no gating (tests).
	mode func() domain.MaintenanceMode
}

// gate is the FUSE face of the liveness sentinel: while the store is
// Offline (deleted, substituted, or under operator maintenance) every
// operation answers EIO instead of rendering a vanished world from the
// index cache — the exact failure the webview guard closes over HTTP.
func (f *fuseFS) gate() syscall.Errno {
	if f.mode != nil && f.mode() == domain.MaintenanceModeOffline {
		return syscall.EIO
	}
	return 0
}

// node is a single inode at a mount-relative path. path == "" is the
// mount root. Every operation delegates to the VFS by full path; the
// facade does the routing (root view vs service trees vs stats file)
// and enforces read-only trees, so the node tree carries no routing
// knowledge of its own.
type node struct {
	fs.Inode

	fsys *fuseFS
	path string
}

func newRoot(v *vfs.VFS, startedAt time.Time, log *slog.Logger, mode func() domain.MaintenanceMode) *node {
	return &node{fsys: &fuseFS{v: v, startedAt: startedAt, log: slogx.OrDiscard(log), mode: mode}}
}

// logOp records a mutation. Success logs at Debug (visible only under
// --debug); failures log at Error, except the everyday control-flow errnos
// (a missing target, an existing one, a non-empty dir, a read-only tree, …),
// which are normal and stay at Debug. Returns e so call sites tail-return it.
func (n *node) logOp(op, target string, e syscall.Errno) syscall.Errno {
	if e == 0 {
		n.fsys.log.Debug("fuse", "op", op, "path", target)
		return e
	}
	level := slog.LevelError
	switch e {
	case syscall.ENOENT, syscall.EEXIST, syscall.ENOTEMPTY, syscall.EROFS,
		syscall.EXDEV, syscall.ENOTDIR, syscall.EISDIR, syscall.EPERM, syscall.ENOSYS:
		level = slog.LevelDebug
	}
	n.fsys.log.LogAttrs(context.Background(), level, "fuse",
		slog.String("op", op), slog.String("path", target), slog.String("errno", e.Error()))
	return e
}

func (n *node) childPath(name string) string {
	return pathx.Join(n.path, cleanName(name))
}

// Compile-time guards: the fs interfaces node implements.
var (
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeCreater   = (*node)(nil)
	_ fs.NodeMkdirer   = (*node)(nil)
	_ fs.NodeUnlinker  = (*node)(nil)
	_ fs.NodeRmdirer   = (*node)(nil)
	_ fs.NodeRenamer   = (*node)(nil)
	_ fs.NodeSetattrer = (*node)(nil)
)

func (n *node) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if e := n.fsys.gate(); e != 0 {
		return e
	}
	if n.path == "" {
		t := uint64(n.fsys.startedAt.Unix())
		out.Mode = fuse.S_IFDIR | 0o755
		out.Mtime, out.Ctime, out.Atime = t, t, t
		return 0
	}
	fi, err := n.fsys.v.Stat(ctx, n.path)
	if err != nil {
		return errnoFromError(err)
	}
	fillAttr(&out.Attr, fi)
	return 0
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if e := n.fsys.gate(); e != 0 {
		return nil, e
	}
	child := n.childPath(name)
	fi, err := n.fsys.v.Stat(ctx, child)
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillAttr(&out.Attr, fi)
	mode := modeBits(fi)
	ino := inodeForPath(child)
	out.Ino = ino
	c := &node{fsys: n.fsys, path: child}
	return n.NewInode(ctx, c, fs.StableAttr{Mode: mode, Ino: ino}), 0
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if e := n.fsys.gate(); e != 0 {
		return nil, e
	}
	d, err := n.fsys.v.OpenFile(ctx, n.path, os.O_RDONLY, 0)
	if err != nil {
		return nil, errnoFromError(err)
	}
	defer d.Close()
	infos, err := d.Readdir(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errnoFromError(err)
	}
	entries := make([]fuse.DirEntry, 0, len(infos))
	for _, fi := range infos {
		entries = append(entries, fuse.DirEntry{
			Mode: modeBits(fi),
			Name: fi.Name(),
			Ino:  inodeForPath(pathx.Join(n.path, fi.Name())),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if e := n.fsys.gate(); e != 0 {
		return nil, 0, e
	}
	f, err := n.fsys.v.OpenFileAt(ctx, n.path, int(flags), 0)
	if err != nil {
		return nil, 0, errnoFromError(err)
	}
	return newVFSFileHandle(f), 0, 0
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	child := n.childPath(name)
	f, err := n.fsys.v.OpenFileAt(ctx, child, int(flags)|os.O_CREATE, os.FileMode(mode))
	if err != nil {
		return nil, nil, 0, n.logOp("create", child, errnoFromError(err))
	}
	fi, err := n.fsys.v.Stat(ctx, child)
	if err != nil {
		_ = f.Close()
		return nil, nil, 0, n.logOp("create", child, syscall.EIO)
	}
	fillAttr(&out.Attr, fi)
	ino := inodeForPath(child)
	c := &node{fsys: n.fsys, path: child}
	inode := n.NewInode(ctx, c, fs.StableAttr{Mode: fuse.S_IFREG, Ino: ino})
	n.logOp("create", child, 0)
	return inode, newVFSFileHandle(f), 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	child := n.childPath(name)
	if err := n.fsys.v.Mkdir(ctx, child, os.FileMode(mode)); err != nil {
		return nil, n.logOp("mkdir", child, errnoFromError(err))
	}
	fi, err := n.fsys.v.Stat(ctx, child)
	if err != nil {
		return nil, n.logOp("mkdir", child, errnoFromError(err))
	}
	fillAttr(&out.Attr, fi)
	ino := inodeForPath(child)
	c := &node{fsys: n.fsys, path: child}
	n.logOp("mkdir", child, 0)
	return n.NewInode(ctx, c, fs.StableAttr{Mode: fuse.S_IFDIR, Ino: ino}), 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	child := n.childPath(name)
	if err := n.fsys.v.RemoveAll(ctx, child); err != nil {
		return n.logOp("unlink", child, errnoFromError(err))
	}
	return n.logOp("unlink", child, 0)
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	child := n.childPath(name)
	if err := n.fsys.v.RemoveAll(ctx, child); err != nil {
		return n.logOp("rmdir", child, errnoFromError(err))
	}
	return n.logOp("rmdir", child, 0)
}

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	np, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}
	from, to := n.childPath(name), np.childPath(newName)
	if err := n.fsys.v.Rename(ctx, from, to); err != nil {
		return n.logOp("rename", from+" -> "+to, errnoFromError(err))
	}
	return n.logOp("rename", from+" -> "+to, 0)
}

func (n *node) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok {
		if err := n.fsys.v.Truncate(ctx, n.path, int64(size)); err != nil {
			return n.logOp("setattr", n.path, errnoFromError(err))
		}
	}
	var attrs vfs.Attrs
	changed := false
	if mode, ok := in.GetMode(); ok {
		m := mode
		attrs.Mode = &m
		changed = true
	}
	if uid, ok := in.GetUID(); ok {
		u := uid
		attrs.UID = &u
		changed = true
	}
	if gid, ok := in.GetGID(); ok {
		g := gid
		attrs.GID = &g
		changed = true
	}
	if mtime, ok := in.GetMTime(); ok {
		t := mtime
		attrs.ModTime = &t
		changed = true
	}
	if changed {
		if err := n.fsys.v.Setattr(ctx, n.path, attrs); err != nil {
			return n.logOp("setattr", n.path, errnoFromError(err))
		}
	}
	fi, err := n.fsys.v.Stat(ctx, n.path)
	if err != nil {
		return n.logOp("setattr", n.path, errnoFromError(err))
	}
	fillAttr(&out.Attr, fi)
	return n.logOp("setattr", n.path, 0)
}

// --- helpers ---

// modeBits returns the FUSE S_IF* type bits for an os.FileInfo.
func modeBits(fi os.FileInfo) uint32 {
	if fi.IsDir() {
		return fuse.S_IFDIR
	}
	return fuse.S_IFREG
}

// fillAttr populates a fuse.Attr from an os.FileInfo returned by the
// VFS facade. UID/GID come from the optional vfs.PosixOwner facet
// (root-view files carry real ownership; synthetic and service-tree
// infos report 0/0, i.e. root-owned).
func fillAttr(out *fuse.Attr, fi os.FileInfo) {
	out.Mode = modeBits(fi) | uint32(fi.Mode().Perm())
	out.Size = uint64(fi.Size())
	t := uint64(fi.ModTime().Unix())
	out.Mtime, out.Ctime, out.Atime = t, t, t
	if o, ok := fi.(vfs.PosixOwner); ok {
		out.Owner.Uid = o.OwnerUID()
		out.Owner.Gid = o.OwnerGID()
	}
}

// errnoFromError translates a Go error into a syscall.Errno for FUSE.
// Wrapped projection sentinels and the io/fs path errors the facade
// returns are recognised via errors.Is.
func errnoFromError(err error) syscall.Errno {
	if err == nil || errors.Is(err, io.EOF) {
		return 0
	}
	switch {
	// Specific projection sentinels first. Several of these bridge to
	// io/fs errors (ErrScratchQuota and ErrEditingDisabled both bridge
	// fs.ErrPermission), so they must be matched before the io/fs
	// fallbacks below — otherwise a quota error would surface as EROFS.
	case errors.Is(err, errs.ErrPathNotFound):
		return syscall.ENOENT
	case errors.Is(err, errs.ErrPathExists):
		return syscall.EEXIST
	case errors.Is(err, errs.ErrIsADirectory):
		return syscall.EISDIR
	case errors.Is(err, errs.ErrNotADirectory):
		return syscall.ENOTDIR
	case errors.Is(err, errs.ErrNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, errs.ErrInvalidPath):
		return syscall.EINVAL
	case errors.Is(err, errs.ErrScratchQuota):
		return syscall.ENOSPC
	case errors.Is(err, errs.ErrEditingDisabled):
		return syscall.EROFS
	case errors.Is(err, errs.ErrArtifactUnreadable), errors.Is(err, errs.ErrSourceUnavailable):
		return syscall.EIO
	// io/fs fallbacks the VFS facade can return directly (e.g.
	// fs.ErrNotExist for a rejected route).
	case errors.Is(err, iofs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, iofs.ErrPermission):
		return syscall.EROFS
	}
	return syscall.EIO
}

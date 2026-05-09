//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/rkurbatov/scrinium/pathx"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/projection"
)

// rootNode is the inode at the FUSE mount point. It dispatches
// every Lookup/Readdir against the routing config: most names
// land in the root view (delegated to FSOps), the service prefix
// surfaces a synthesised directory of trees + stats + raw mirror.
//
// rootNode itself never holds artifact data; it is a pure
// dispatch point. The expensive state (View, FSOps) lives behind
// the pointers it carries.
type rootNode struct {
	fs.Inode

	view       *projection.View
	fsops      *projection.FSOps
	store      projection.StoreClient
	routingCfg projection.RoutingConfig
	startedAt  time.Time
	// statsProvider, if non-nil, is the function rendering the
	// _scrinium/stats virtual file. mount_fuse.go injects one
	// that bundles capacity + extensions; tests that build a
	// rootNode directly leave it nil and get the default
	// View-only render.
	statsProvider func() []byte
}

// Compile-time guards: which fs interfaces rootNode implements.
var (
	_ fs.NodeLookuper  = (*rootNode)(nil)
	_ fs.NodeReaddirer = (*rootNode)(nil)
	_ fs.NodeGetattrer = (*rootNode)(nil)
	_ fs.NodeCreater   = (*rootNode)(nil)
	_ fs.NodeMkdirer   = (*rootNode)(nil)
	_ fs.NodeUnlinker  = (*rootNode)(nil)
	_ fs.NodeRmdirer   = (*rootNode)(nil)
	_ fs.NodeRenamer   = (*rootNode)(nil)
)

func (r *rootNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o755
	out.Size = 0
	now := uint64(r.startedAt.Unix())
	out.Mtime, out.Ctime, out.Atime = now, now, now
	return 0
}

func (r *rootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	target, err := projection.Route(name, r.routingCfg)
	if err != nil {
		if errors.Is(err, projection.ErrRouteRejected) {
			return nil, syscall.ENOENT
		}
		return nil, errnoFromError(err)
	}
	switch target.Kind {
	case projection.RouteRoot:
		return r.lookupRoot(ctx, target.SubPath, out)
	case projection.RouteServiceRoot:
		return r.lookupServiceRoot(ctx, out)
	case projection.RouteServiceTree:
		return r.lookupServiceTree(ctx, target.Tree, target.SubPath, out)
	case projection.RouteStatsFile:
		return r.lookupStatsFile(ctx, out)
	case projection.RouteRawMirror:
		// Raw mirror lands in 5c.
		return nil, syscall.ENOSYS
	}
	return nil, syscall.ENOENT
}

// lookupRoot resolves a name within the configured root view by
// delegating to FSOps.Stat and producing a treeNode child.
func (r *rootNode) lookupRoot(ctx context.Context, sub string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	fi, err := r.fsops.Stat(sub)
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillAttr(&out.Attr, fi)

	child := &treeNode{
		root:     r,
		tree:     r.routingCfg.RootView,
		subPath:  sub,
		readOnly: false,
	}
	mode := uint32(fuse.S_IFREG)
	if fi.IsDir {
		mode = fuse.S_IFDIR
	}
	inode := r.NewInode(ctx, child, fs.StableAttr{
		Mode: mode,
		Ino:  inodeFor(string(r.routingCfg.RootView), sub),
	})
	return inode, 0
}

// lookupServiceRoot returns the service-prefix directory inode.
func (r *rootNode) lookupServiceRoot(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	out.Mode = fuse.S_IFDIR | 0o555
	now := uint64(r.startedAt.Unix())
	out.Mtime, out.Ctime, out.Atime = now, now, now
	out.Ino = 2
	child := &serviceRootNode{root: r}
	inode := r.NewInode(ctx, child, fs.StableAttr{
		Mode: fuse.S_IFDIR,
		Ino:  2,
	})
	return inode, 0
}

func (r *rootNode) lookupServiceTree(ctx context.Context, tree projection.RootView, sub string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Look up directly in the View, bypass FSOps (read-only).
	node, err := r.view.GetIn(tree, sub)
	if err != nil {
		return nil, errnoFromError(err)
	}
	mode := uint32(fuse.S_IFREG)
	if node.FS.IsDir {
		mode = fuse.S_IFDIR
	}
	out.Mode = mode | 0o555
	out.Size = uint64(node.FS.Size)
	t := uint64(node.FS.ModTime.Unix())
	if t == 0 {
		t = uint64(r.startedAt.Unix())
	}
	out.Mtime, out.Ctime, out.Atime = t, t, t
	ino := inodeFor(string(tree), sub)
	out.Ino = ino

	child := &treeNode{
		root:     r,
		tree:     tree,
		subPath:  sub,
		readOnly: true,
	}
	inode := r.NewInode(ctx, child, fs.StableAttr{Mode: mode, Ino: ino})
	return inode, 0
}

func (r *rootNode) lookupStatsFile(ctx context.Context, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	body := r.statsBody()
	out.Mode = fuse.S_IFREG | 0o444
	out.Size = uint64(len(body))
	now := uint64(time.Now().Unix())
	out.Mtime, out.Ctime, out.Atime = now, now, now
	out.Ino = 3

	child := &statsNode{root: r}
	inode := r.NewInode(ctx, child, fs.StableAttr{
		Mode: fuse.S_IFREG,
		Ino:  3,
	})
	return inode, 0
}

func (r *rootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Root listing: every immediate child of "" in the root view,
	// plus the service prefix entry if enabled.
	var entries []fuse.DirEntry

	for fi, err := range r.fsops.Listdir("") {
		if err != nil {
			return nil, errnoFromError(err)
		}
		mode := uint32(fuse.S_IFREG)
		if fi.IsDir {
			mode = fuse.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{
			Mode: mode,
			Name: fi.Name,
			Ino:  inodeFor(string(r.routingCfg.RootView), fi.Path),
		})
	}

	if r.routingCfg.ServicePrefix != "" {
		entries = append(entries, fuse.DirEntry{
			Mode: fuse.S_IFDIR,
			Name: r.routingCfg.ServicePrefix,
			Ino:  2,
		})
	}
	return fs.NewListDirStream(entries), 0
}

// Mutations on root: delegate to FSOps. Each method first checks
// the path is not a service-prefix collision (you can't create
// a file named "_scrinium" at the root).
func (r *rootNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if projection.IsServicePath(name, r.routingCfg) {
		return nil, nil, 0, syscall.EACCES
	}
	f, err := r.fsops.Create(ctx, name, mode)
	if err != nil {
		return nil, nil, 0, errnoFromError(err)
	}
	fi, err := r.fsops.Stat(name)
	if err != nil {
		// Should be impossible right after a successful Create
		// of an empty file; surface as I/O error if it happens.
		_ = f.Close()
		return nil, nil, 0, syscall.EIO
	}
	fillAttr(&out.Attr, fi)
	child := &treeNode{root: r, tree: r.routingCfg.RootView, subPath: name}
	inode := r.NewInode(ctx, child, fs.StableAttr{
		Mode: fuse.S_IFREG,
		Ino:  inodeFor(string(r.routingCfg.RootView), name),
	})
	return inode, &scriniumFileHandle{file: f}, 0, 0
}

func (r *rootNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if projection.IsServicePath(name, r.routingCfg) {
		return nil, syscall.EACCES
	}
	if err := r.fsops.Mkdir(name, mode); err != nil {
		return nil, errnoFromError(err)
	}
	out.Mode = fuse.S_IFDIR | (mode & 0o777)
	now := uint64(time.Now().Unix())
	out.Mtime, out.Ctime, out.Atime = now, now, now
	out.Ino = inodeFor(string(r.routingCfg.RootView), name)

	child := &treeNode{root: r, tree: r.routingCfg.RootView, subPath: name}
	inode := r.NewInode(ctx, child, fs.StableAttr{
		Mode: fuse.S_IFDIR,
		Ino:  out.Ino,
	})
	return inode, 0
}

func (r *rootNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if projection.IsServicePath(name, r.routingCfg) {
		return syscall.EACCES
	}
	if err := r.fsops.Unlink(ctx, name); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (r *rootNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if projection.IsServicePath(name, r.routingCfg) {
		return syscall.EACCES
	}
	if err := r.fsops.Rmdir(name); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (r *rootNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// Rename across parent inodes is not straightforward in our
	// model — the path is constructed by walking inode chains.
	// 5b restricts rename to within the root view: newParent must
	// be the root or a treeNode in the root view.
	oldFull := name
	newFull, ok := r.fullPathFor(newParent, newName)
	if !ok {
		return syscall.EXDEV
	}
	if projection.IsServicePath(newFull, r.routingCfg) {
		return syscall.EACCES
	}
	if err := r.fsops.Rename(ctx, oldFull, newFull); err != nil {
		return errnoFromError(err)
	}
	return 0
}

// fullPathFor reconstructs the absolute virtual path of a parent
// inode within the root view. Returns ok=false if the parent is
// not in the root view (service trees, etc.) — rename across
// trees is rejected as EXDEV.
func (r *rootNode) fullPathFor(parent fs.InodeEmbedder, name string) (string, bool) {
	if _, isRoot := parent.(*rootNode); isRoot {
		return name, true
	}
	tn, ok := parent.(*treeNode)
	if !ok || tn.tree != r.routingCfg.RootView || tn.readOnly {
		return "", false
	}
	if tn.subPath == "" {
		return name, true
	}
	return tn.subPath + "/" + name, true
}

// statsBody renders the current ViewStats. Delegates to the
// daemon-installed statsProvider when present (which uses
// projection.RenderStats with full DaemonInfo); falls back to a
// minimal View-only render so tests without a provider still
// see meaningful output.
func (r *rootNode) statsBody() []byte {
	if r.statsProvider != nil {
		return r.statsProvider()
	}
	return projection.RenderStats(r.view, projection.DaemonInfo{
		StartedAt: r.startedAt,
	})
}

// --- treeNode: any directory or file inside a tree (root or
// service). Holds (tree, subPath, readOnly).

type treeNode struct {
	fs.Inode

	root     *rootNode
	tree     projection.RootView
	subPath  string
	readOnly bool
}

var (
	_ fs.NodeLookuper  = (*treeNode)(nil)
	_ fs.NodeReaddirer = (*treeNode)(nil)
	_ fs.NodeGetattrer = (*treeNode)(nil)
	_ fs.NodeOpener    = (*treeNode)(nil)
)

func (n *treeNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if n.readOnly || n.tree != n.root.routingCfg.RootView {
		// Service-tree node: read from View.
		node, err := n.root.view.GetIn(n.tree, n.subPath)
		if err != nil {
			return errnoFromError(err)
		}
		fillAttr(&out.Attr, projection.FileInfo{
			Name:    node.FS.Name,
			Path:    node.FS.Path,
			Size:    node.FS.Size,
			ModTime: node.FS.ModTime,
			IsDir:   node.FS.IsDir,
			Mode:    0o555,
		})
		return 0
	}
	// Root view: ask FSOps for the schema-aware FileInfo.
	fi, err := n.root.fsops.Stat(n.subPath)
	if err != nil {
		return errnoFromError(err)
	}
	fillAttr(&out.Attr, fi)
	return 0
}

func (n *treeNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	child := pathx.Join(n.subPath, cleanName(name))
	if n.readOnly || n.tree != n.root.routingCfg.RootView {
		node, err := n.root.view.GetIn(n.tree, child)
		if err != nil {
			return nil, errnoFromError(err)
		}
		mode := uint32(fuse.S_IFREG)
		if node.FS.IsDir {
			mode = fuse.S_IFDIR
		}
		out.Mode = mode | 0o555
		out.Size = uint64(node.FS.Size)
		t := uint64(node.FS.ModTime.Unix())
		if t == 0 {
			t = uint64(n.root.startedAt.Unix())
		}
		out.Mtime, out.Ctime, out.Atime = t, t, t
		ino := inodeFor(string(n.tree), child)
		out.Ino = ino
		nc := &treeNode{root: n.root, tree: n.tree, subPath: child, readOnly: true}
		return n.NewInode(ctx, nc, fs.StableAttr{Mode: mode, Ino: ino}), 0
	}
	fi, err := n.root.fsops.Stat(child)
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillAttr(&out.Attr, fi)
	mode := uint32(fuse.S_IFREG)
	if fi.IsDir {
		mode = fuse.S_IFDIR
	}
	nc := &treeNode{root: n.root, tree: n.tree, subPath: child}
	ino := inodeFor(string(n.tree), child)
	return n.NewInode(ctx, nc, fs.StableAttr{Mode: mode, Ino: ino}), 0
}

func (n *treeNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var entries []fuse.DirEntry
	if n.readOnly || n.tree != n.root.routingCfg.RootView {
		for child, err := range n.root.view.ListIn(n.tree, n.subPath) {
			if err != nil {
				return nil, errnoFromError(err)
			}
			mode := uint32(fuse.S_IFREG)
			if child.FS.IsDir {
				mode = fuse.S_IFDIR
			}
			entries = append(entries, fuse.DirEntry{
				Mode: mode,
				Name: child.FS.Name,
				Ino:  inodeFor(string(n.tree), child.FS.Path),
			})
		}
		return fs.NewListDirStream(entries), 0
	}
	for fi, err := range n.root.fsops.Listdir(n.subPath) {
		if err != nil {
			return nil, errnoFromError(err)
		}
		mode := uint32(fuse.S_IFREG)
		if fi.IsDir {
			mode = fuse.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{
			Mode: mode,
			Name: fi.Name,
			Ino:  inodeFor(string(n.tree), fi.Path),
		})
	}
	return fs.NewListDirStream(entries), 0
}

// Open: read for service trees, read/write for root view (subject
// to editing policy in FSOps).
func (n *treeNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.readOnly || n.tree != n.root.routingCfg.RootView {
		// Direct open via View — FSOps would route the same way
		// but service-tree paths aren't in the FSOps root tree
		// universe.
		rh, err := n.root.view.OpenIn(ctx, n.tree, n.subPath, domain.GetOptions{})
		if err != nil {
			return nil, 0, errnoFromError(err)
		}
		return &readHandleFile{rh: rh}, 0, 0
	}
	mode := openModeFromFlags(flags)
	f, err := n.root.fsops.Open(ctx, n.subPath, mode)
	if err != nil {
		return nil, 0, errnoFromError(err)
	}
	return &scriniumFileHandle{file: f}, 0, 0
}

// Setattr handles chmod/chown/utimens/truncate from POSIX.
// Only meaningful for root-view nodes.
func (n *treeNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if n.readOnly || n.tree != n.root.routingCfg.RootView {
		return syscall.EROFS
	}
	// Truncate via Setattr's size field.
	if size, ok := in.GetSize(); ok {
		if err := n.root.fsops.Truncate(ctx, n.subPath, int64(size)); err != nil {
			return errnoFromError(err)
		}
	}
	// Other attribute changes go through Setattr.
	var attrs projection.Attrs
	any := false
	if mode, ok := in.GetMode(); ok {
		m := mode
		attrs.Mode = &m
		any = true
	}
	if uid, ok := in.GetUID(); ok {
		u := uid
		attrs.UID = &u
		any = true
	}
	if gid, ok := in.GetGID(); ok {
		g := gid
		attrs.GID = &g
		any = true
	}
	if mtime, ok := in.GetMTime(); ok {
		t := mtime
		attrs.ModTime = &t
		any = true
	}
	if any {
		if err := n.root.fsops.Setattr(ctx, n.subPath, attrs); err != nil {
			return errnoFromError(err)
		}
	}
	// Return the updated attributes.
	fi, err := n.root.fsops.Stat(n.subPath)
	if err != nil {
		return errnoFromError(err)
	}
	fillAttr(&out.Attr, fi)
	return 0
}

var _ fs.NodeSetattrer = (*treeNode)(nil)

// --- serviceRootNode: directory listing of enabled service trees.

type serviceRootNode struct {
	fs.Inode
	root *rootNode
}

var (
	_ fs.NodeLookuper  = (*serviceRootNode)(nil)
	_ fs.NodeReaddirer = (*serviceRootNode)(nil)
	_ fs.NodeGetattrer = (*serviceRootNode)(nil)
)

func (s *serviceRootNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o555
	now := uint64(s.root.startedAt.Unix())
	out.Mtime, out.Ctime, out.Atime = now, now, now
	return 0
}

// childrenOfServiceRoot returns the list of enabled service-tree
// names. Order is stable across mounts so users can rely on tab
// completion.
func (s *serviceRootNode) children() []string {
	cfg := s.root.routingCfg
	var out []string
	if cfg.ShowStats {
		out = append(out, "stats")
	}
	if cfg.ShowByArtifact {
		out = append(out, "by-artifact")
	}
	if cfg.ShowByDate {
		out = append(out, "by-date")
	}
	if cfg.ShowBySession {
		out = append(out, "by-session")
	}
	if cfg.ShowByNamespace {
		out = append(out, "by-namespace")
	}
	if cfg.ShowOrphaned {
		out = append(out, "orphaned")
	}
	if cfg.ShowRaw {
		out = append(out, "raw")
	}
	// "by-path" is always exposed inside _scrinium when the
	// service prefix is enabled (allows surfacing the path tree
	// even when root-view is by-something-else).
	out = append(out, "by-path")
	return out
}

func (s *serviceRootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var entries []fuse.DirEntry
	for _, name := range s.children() {
		entries = append(entries, fuse.DirEntry{
			Mode: fuse.S_IFDIR,
			Name: name,
			Ino:  inodeFor("__service__", name),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (s *serviceRootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Reuse rootNode dispatch by composing the full path.
	full := s.root.routingCfg.ServicePrefix + "/" + name
	target, err := projection.Route(full, s.root.routingCfg)
	if err != nil {
		if errors.Is(err, projection.ErrRouteRejected) {
			return nil, syscall.ENOENT
		}
		return nil, errnoFromError(err)
	}
	switch target.Kind {
	case projection.RouteServiceTree:
		// Tree root entry. Build a treeNode pointing at the tree
		// with empty subPath.
		out.Mode = fuse.S_IFDIR | 0o555
		now := uint64(s.root.startedAt.Unix())
		out.Mtime, out.Ctime, out.Atime = now, now, now
		ino := inodeFor(string(target.Tree), "")
		out.Ino = ino
		nc := &treeNode{root: s.root, tree: target.Tree, subPath: "", readOnly: true}
		return s.NewInode(ctx, nc, fs.StableAttr{Mode: fuse.S_IFDIR, Ino: ino}), 0
	case projection.RouteStatsFile:
		body := s.root.statsBody()
		out.Mode = fuse.S_IFREG | 0o444
		out.Size = uint64(len(body))
		now := uint64(time.Now().Unix())
		out.Mtime, out.Ctime, out.Atime = now, now, now
		out.Ino = 3
		nc := &statsNode{root: s.root}
		return s.NewInode(ctx, nc, fs.StableAttr{Mode: fuse.S_IFREG, Ino: 3}), 0
	case projection.RouteRawMirror:
		return nil, syscall.ENOSYS // 5c
	}
	return nil, syscall.ENOENT
}

// --- statsNode: virtual stats file. Body is regenerated on each
// read; we serve from a snapshot taken at Open to keep reads
// consistent within a session.

type statsNode struct {
	fs.Inode
	root *rootNode
}

var (
	_ fs.NodeOpener    = (*statsNode)(nil)
	_ fs.NodeGetattrer = (*statsNode)(nil)
)

func (s *statsNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	body := s.root.statsBody()
	out.Mode = fuse.S_IFREG | 0o444
	out.Size = uint64(len(body))
	now := uint64(time.Now().Unix())
	out.Mtime, out.Ctime, out.Atime = now, now, now
	return 0
}

func (s *statsNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	body := s.root.statsBody()
	return &bytesFileHandle{body: body}, fuse.FOPEN_DIRECT_IO, 0
}

// --- File handles ---

// scriniumFileHandle wraps a projection.File for FUSE Read/Write/
// Release. Holds a mutex around the underlying file because go-fuse
// may dispatch concurrent Read/Write on the same handle.
type scriniumFileHandle struct {
	mu   sync.Mutex
	file projection.File
}

var (
	_ fs.FileReader   = (*scriniumFileHandle)(nil)
	_ fs.FileWriter   = (*scriniumFileHandle)(nil)
	_ fs.FileFlusher  = (*scriniumFileHandle)(nil)
	_ fs.FileReleaser = (*scriniumFileHandle)(nil)
	_ fs.FileFsyncer  = (*scriniumFileHandle)(nil)
)

func (h *scriniumFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.file.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errnoFromError(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *scriniumFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.file.WriteAt(data, off)
	if err != nil {
		return uint32(n), errnoFromError(err)
	}
	return uint32(n), 0
}

func (h *scriniumFileHandle) Flush(ctx context.Context) syscall.Errno {
	// FUSE Flush is called on every close(2) — but we only want
	// to materialise the artifact in Release. Sync here would
	// be premature. Return 0 to acknowledge.
	return 0
}

func (h *scriniumFileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.file.Sync(); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (h *scriniumFileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.file.Close(); err != nil {
		return errnoFromError(err)
	}
	return 0
}

// readHandleFile wraps a core.ReadHandle for read-only service
// tree opens. Simpler than scriniumFileHandle — no Write/Flush.
type readHandleFile struct {
	mu sync.Mutex
	rh core.ReadHandle
}

var (
	_ fs.FileReader   = (*readHandleFile)(nil)
	_ fs.FileReleaser = (*readHandleFile)(nil)
)

func (h *readHandleFile) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.rh.SupportsRandomAccess() {
		return nil, syscall.ENOTSUP
	}
	n, err := h.rh.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errnoFromError(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *readHandleFile) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.rh.Close(); err != nil {
		return errnoFromError(err)
	}
	return 0
}

// bytesFileHandle is a read-only handle backed by a static byte
// slice. Used for the stats file.
type bytesFileHandle struct {
	body []byte
}

var (
	_ fs.FileReader   = (*bytesFileHandle)(nil)
	_ fs.FileReleaser = (*bytesFileHandle)(nil)
)

func (h *bytesFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(h.body)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(h.body)) {
		end = int64(len(h.body))
	}
	return fuse.ReadResultData(h.body[off:end]), 0
}

func (h *bytesFileHandle) Release(ctx context.Context) syscall.Errno { return 0 }

// --- Helpers ---

// errnoFromError translates a Go error into a syscall.Errno
// suitable for returning to FUSE. Wrapped projection sentinels
// are recognised via errors.Is.
func errnoFromError(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	if errors.Is(err, io.EOF) {
		return 0
	}
	switch {
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
	case errors.Is(err, errs.ErrEditingDisabled):
		return syscall.EROFS
	case errors.Is(err, errs.ErrScratchQuota):
		return syscall.ENOSPC
	case errors.Is(err, errs.ErrViewClosed):
		return syscall.EIO
	case errors.Is(err, errs.ErrArtifactUnreadable):
		return syscall.EIO
	case errors.Is(err, errs.ErrSourceUnavailable):
		return syscall.EIO
	}
	return syscall.EIO
}

// fillAttr populates a fuse.Attr from a projection.FileInfo.
// Used both by Lookup-side fills (through &entry.Attr) and
// Getattr-side fills (through &attr.Attr); the embedded Attr
// is identical in fuse.EntryOut and fuse.AttrOut.
func fillAttr(out *fuse.Attr, fi projection.FileInfo) {
	mode := uint32(fuse.S_IFREG)
	if fi.IsDir {
		mode = fuse.S_IFDIR
	}
	out.Mode = mode | (fi.Mode & 0o7777)
	out.Size = uint64(fi.Size)
	t := uint64(fi.ModTime.Unix())
	out.Mtime, out.Ctime, out.Atime = t, t, t
	out.Owner.Uid = fi.UID
	out.Owner.Gid = fi.GID
}

// openModeFromFlags translates POSIX open(2) flags to projection
// OpenMode bits. We honour O_RDONLY/O_WRONLY/O_RDWR and O_APPEND;
// other flags (O_TRUNC, O_CREAT, O_EXCL) are handled by the FUSE
// kernel layer before our Open is called.
func openModeFromFlags(flags uint32) projection.OpenMode {
	// POSIX bits are not exposed in syscall consistently across
	// platforms; we use the canonical numerics.
	const (
		oRDONLY = 0x0
		oWRONLY = 0x1
		oRDWR   = 0x2
		oAPPEND = 0x400
	)
	var m projection.OpenMode
	switch flags & 0x3 {
	case oRDONLY:
		m = projection.OpenReadOnly
	case oWRONLY:
		m = projection.OpenWriteOnly
	case oRDWR:
		m = projection.OpenReadWrite
	}
	if flags&oAPPEND != 0 {
		m |= projection.OpenAppend
	}
	return m
}

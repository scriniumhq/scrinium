package vfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	"scrinium.dev/projection"
	fso "scrinium.dev/projection/internal/fsops"
	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/projection/pathx"

	"scrinium.dev/errs"
)

// POSIX flag bits used for OpenFile semantics. Aliased to
// syscall constants for the platforms we support; the values
// are stable on Linux/macOS/Windows for these particular flags.
const (
	syscallOCreate = syscall.O_CREAT
	syscallOExcl   = syscall.O_EXCL
)

// VFS exposes a vw.View + fso.Ops as a
// virtual filesystem. Surfaces (FUSE, WebDAV, web view, custom
// admin tools) consume Stat / OpenFile / Readdir without
// reaching into the projection internals.
//
// Concurrency: VFS is safe for concurrent use. Per-file state
// is owned by the File implementations returned from OpenFile.
//
// VFS is stateless beyond the configuration captured at
// construction; recreate or reuse freely. The View / FSOps
// references must outlive the VFS — the daemon owns those
// lifetimes.
type VFS struct {
	view       *vw.View
	fsops      *fso.Ops
	routingCfg Config
	// rootView is the tree that backs the mount root. Derived
	// once from view.RootView() so routing agrees with FSOps,
	// which resolves the root tree the same way.
	rootView  vw.RootView
	startedAt time.Time

	// provRoots is the set of extension-contributed roots the
	// dispatcher uses to recognise provided-view service trees.
	// ProvidedRoots is fixed once the View is built, so it is
	// materialised once here rather than rebuilt on every Stat /
	// OpenFile (FUSE calls those thousands of times a second).
	provRoots map[vw.RootView]bool

	// statsProvider, if non-nil, returns the bytes served at
	// _scrinium/stats. Hosts inject one that bundles live
	// capacity, custom index list, and policy snippets in
	// addition to the View counters. Falls back to a minimal
	// View-only renderer when nil — keeps tests and basic
	// surfaces working without supplying a provider.
	statsProvider StatsProvider

	// nameFilter, if non-nil, suppresses entries from
	// directory listings whose names it matches. Used for
	// surface-level junk filters (WebDAV's .DS_Store, etc.).
	// Read paths and Stat against filtered names still
	// resolve normally — the surface decides whether to
	// reject or substitute (e.g. a black-hole file).
	nameFilter func(name string) bool
}

// StatsProvider returns the bytes for the _scrinium/stats
// virtual file. Hosts implement this to feed live data
// (capacity, custom indexes, runtime info) that the projection
// alone doesn't track.
type StatsProvider func() []byte

// Option configures VFS construction. Optional knobs only —
// view, fsops, and routingCfg are required positional args.
type Option func(*VFS)

// WithStatsProvider attaches a stats body provider. When set,
// reads against _scrinium/stats serve the bytes returned by
// fn.
func WithStatsProvider(fn StatsProvider) Option {
	return func(v *VFS) { v.statsProvider = fn }
}

// WithNameFilter installs a directory-listing filter. When fn
// returns true for a name, that entry is omitted from
// Readdir output. The filter is applied to root and root-
// subtree listings; service-tree listings are always
// presented in full (the diagnostics surface is meant to see
// everything).
func WithNameFilter(fn func(name string) bool) Option {
	return func(v *VFS) { v.nameFilter = fn }
}

// New constructs a VFS over view/fsops with the given namespace
// configuration. The tree backing the mount root is taken from
// view.RootView().
// New constructs a VFS over a built projection with the given
// namespace configuration. The tree backing the mount root is taken
// from the projection's View. Taking the bundle (rather than the bare
// View and Ops) keeps the projection's internal types out of caller
// code: daemons name only *projection.Projection.
func New(proj *projection.Projection, cfg Config, opts ...Option) *VFS {
	pr := make(map[vw.RootView]bool)
	for _, r := range proj.View.ProvidedRoots() {
		pr[r] = true
	}

	v := &VFS{
		view:       proj.View,
		fsops:      proj.FSOps,
		routingCfg: cfg,
		rootView:   proj.View.RootView(),
		startedAt:  time.Now().UTC(),
		provRoots:  pr,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// CleanPath normalises a slash-style path: drops the leading
// and trailing slash. The result is a tree-relative path
// suitable for route. Surfaces hand raw paths to
// VFS; VFS does its own cleaning so callers don't need to
// duplicate the rule.
func CleanPath(name string) string {
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimSuffix(name, "/")
	return name
}

// --- write methods ---

// Mkdir creates a directory. Service trees are read-only, so
// any path under cfg.ServicePrefix returns ErrEditingDisabled
// (translated to whatever errno layer the caller wants). The
// root view delegates to FSOps.Mkdir.
func (v *VFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	clean := CleanPath(name)
	if isAtServiceRoot(clean, v.routingCfg) {
		return errs.ErrEditingDisabled
	}
	return v.fsops.Mkdir(clean, uint32(perm))
}

// RemoveAll deletes a file or empty directory. Recursive
// deletion of non-empty trees is intentionally not
// implemented — surfaces that promise it (WebDAV's
// RemoveAll) translate the resulting ErrNotEmpty themselves.
func (v *VFS) RemoveAll(ctx context.Context, name string) error {
	clean := CleanPath(name)
	if clean == "" {
		// Refuse to remove the mount root.
		return errs.ErrEditingDisabled
	}
	if isAtServiceRoot(clean, v.routingCfg) {
		return errs.ErrEditingDisabled
	}
	// Try Unlink first (file). If the path is a directory,
	// fall back to Rmdir.
	err := v.fsops.Unlink(ctx, clean)
	if err == nil {
		return nil
	}
	if errors.Is(err, errs.ErrIsADirectory) {
		return v.fsops.Rmdir(clean)
	}
	return err
}

// RemoveTree deletes name and, when name is a directory, its entire subtree —
// the WebDAV DELETE semantics (depth-infinity on a collection, RFC 4918 §9.6).
// It is separate from RemoveAll because POSIX rmdir — and thus the FUSE Rmdir
// path RemoveAll serves — must refuse a non-empty directory; only the WebDAV
// adapter calls this. The mount root and the service-tree root are refused, as
// in RemoveAll.
func (v *VFS) RemoveTree(ctx context.Context, name string) error {
	clean := CleanPath(name)
	if clean == "" {
		return errs.ErrEditingDisabled
	}
	if isAtServiceRoot(clean, v.routingCfg) {
		return errs.ErrEditingDisabled
	}
	return v.fsops.RemoveTree(ctx, clean)
}

// Rename moves a path. Cross-tree renames are not supported:
// destinations under the service prefix are rejected.
func (v *VFS) Rename(ctx context.Context, oldName, newName string) error {
	oldClean := CleanPath(oldName)
	newClean := CleanPath(newName)
	if isAtServiceRoot(newClean, v.routingCfg) {
		return errs.ErrEditingDisabled
	}
	return v.fsops.Rename(ctx, oldClean, newClean)
}

// Attrs carries the mutable metadata Setattr can change; a nil
// field leaves that attribute untouched. VFS owns this type so
// surfaces (FUSE Setattr, future admin tools) don't reach into
// the projection primitive for it.
type Attrs struct {
	Mode    *uint32
	UID     *uint32
	GID     *uint32
	ModTime *time.Time
}

// Truncate resizes a root-view file to size bytes. The mount
// root and service trees are read-only and return
// ErrEditingDisabled.
func (v *VFS) Truncate(ctx context.Context, name string, size int64) error {
	clean := CleanPath(name)
	if clean == "" || isAtServiceRoot(clean, v.routingCfg) {
		return errs.ErrEditingDisabled
	}
	return v.fsops.Truncate(ctx, clean, size)
}

// Setattr applies metadata changes to a root-view path. The
// mount root and service trees are read-only and return
// ErrEditingDisabled. Nil Attrs fields are left untouched.
func (v *VFS) Setattr(ctx context.Context, name string, attrs Attrs) error {
	clean := CleanPath(name)
	if clean == "" || isAtServiceRoot(clean, v.routingCfg) {
		return errs.ErrEditingDisabled
	}
	return v.fsops.Setattr(ctx, clean, fso.Attrs{
		Mode:    attrs.Mode,
		UID:     attrs.UID,
		GID:     attrs.GID,
		ModTime: attrs.ModTime,
	})
}

// --- read methods ---

// Stat returns metadata for a path. Routing is centralised in
// route; VFS just translates the route into a
// FileInfo. Service trees produce synthetic infos for the
// service root and the stats virtual file; the rest go
// through the View.
func (v *VFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	clean := CleanPath(name)
	tgt, err := route(clean, v.routingCfg, v.rootView, v.provRoots)
	if err != nil {
		if errors.Is(err, errRejected) {
			return nil, fs.ErrNotExist
		}
		return nil, err
	}
	switch tgt.Kind {
	case kindRoot:
		fi, err := v.fsops.Stat(tgt.SubPath)
		if err != nil {
			return nil, err
		}
		return projectionFileInfo{fi: fi}, nil
	case kindServiceRoot:
		return synthDirInfo(v.routingCfg.ServicePrefix, v.startedAt), nil
	case kindServiceTree:
		node, err := v.view.GetIn(tgt.Tree, tgt.SubPath)
		if err != nil {
			return nil, err
		}
		return projectionNodeInfo{node: node, fallbackTime: v.startedAt}, nil
	case kindStatsFile:
		body := v.statsBody()
		return synthFileInfo("stats", int64(len(body)), time.Now()), nil
	case kindRawMirror:
		return nil, fs.ErrNotExist // raw mirror not implemented yet
	}
	return nil, fs.ErrNotExist
}

// OpenFile opens a path. Flags follow POSIX: O_CREATE creates,
// O_TRUNC truncates, O_RDWR/O_WRONLY allow writes. Read-only
// callers ignore flag bits and pass 0.
func (v *VFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (File, error) {
	clean := CleanPath(name)
	tgt, err := route(clean, v.routingCfg, v.rootView, v.provRoots)
	if err != nil {
		if errors.Is(err, errRejected) {
			return nil, fs.ErrNotExist
		}
		return nil, err
	}

	wantCreate := flag&syscallOCreate != 0
	wantTrunc := flag&os.O_TRUNC != 0
	wantWrite := flag&(os.O_WRONLY|os.O_RDWR) != 0
	_ = wantTrunc

	switch tgt.Kind {
	case kindRoot:
		return v.openRoot(ctx, tgt.SubPath, flag, perm, wantCreate, wantWrite)

	case kindServiceRoot:
		if wantWrite || wantCreate {
			return nil, errs.ErrEditingDisabled
		}
		return newServiceDirFile(v, v.routingCfg.ServicePrefix, "", true), nil

	case kindServiceTree:
		if wantWrite || wantCreate {
			return nil, errs.ErrEditingDisabled
		}
		node, err := v.view.GetIn(tgt.Tree, tgt.SubPath)
		if err != nil {
			return nil, err
		}
		if node.FS.IsDir {
			return newServiceDirFile(v, tgt.Tree, tgt.SubPath, false), nil
		}
		rh, err := v.view.OpenIn(ctx, tgt.Tree, tgt.SubPath)
		if err != nil {
			return nil, err
		}
		return &readHandleFile{
			rh:    rh,
			name:  pathx.LastSegment(tgt.SubPath),
			path:  tgt.SubPath,
			size:  node.FS.Size,
			mtime: nodeModTime(node, v.startedAt),
			isDir: false,
		}, nil

	case kindStatsFile:
		if wantWrite || wantCreate {
			return nil, errs.ErrEditingDisabled
		}
		body := v.statsBody()
		return newBytesFile("stats", body, time.Now()), nil

	case kindRawMirror:
		return nil, fs.ErrNotExist
	}
	return nil, fs.ErrNotExist
}

// OpenFileAt opens a regular file for positioned IO. It is the
// FUSE-facing entry point: the kernel reads and writes file
// content by offset (ReadAt/WriteAt) rather than through the
// streaming cursor that the sequential File from OpenFile
// provides.
//
// Routing, flag handling, and read-only rejection are exactly
// OpenFile's — this delegates and then narrows the result to
// FileAt. Directory paths (mount root, service root, service
// subtrees, root-view directories) return errs.ErrIsADirectory:
// callers list directories through Stat plus the dir File from
// OpenFile, never through OpenFileAt.
func (v *VFS) OpenFileAt(ctx context.Context, name string, flag int, perm os.FileMode) (FileAt, error) {
	f, err := v.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}
	fa, ok := f.(FileAt)
	if !ok {
		_ = f.Close()
		return nil, errs.ErrIsADirectory
	}
	return fa, nil
}

// --- helpers ---

// isAtServiceRoot reports whether a path is exactly the
// service prefix or anything under it. Service tree paths
// are read-only; mutating methods reject them.
func isAtServiceRoot(clean string, cfg Config) bool {
	if cfg.ServicePrefix == "" {
		return false
	}
	return pathx.IsUnder(clean, cfg.ServicePrefix)
}

// nodeModTime returns the View node's modification time, or
// fallback when the node has no time recorded.
func nodeModTime(n vw.Node, fallback time.Time) time.Time {
	if !n.FS.ModTime.IsZero() {
		return n.FS.ModTime
	}
	return fallback
}

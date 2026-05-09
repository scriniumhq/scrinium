package vfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/projection"
	"github.com/rkurbatov/scrinium/pathx"
)

// POSIX flag bits used for OpenFile semantics. Aliased to
// syscall constants for the platforms we support; the values
// are stable on Linux/macOS/Windows for these particular flags.
const (
	syscallOCreate = syscall.O_CREAT
	syscallOExcl   = syscall.O_EXCL
)

// VFS exposes a projection.View + projection.FSOps as a
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
	view       *projection.View
	fsops      *projection.FSOps
	routingCfg projection.RoutingConfig
	startedAt  time.Time

	// statsProvider, if non-nil, returns the bytes served at
	// _scrinium/stats. Hosts inject one that bundles live
	// capacity, extension list, and policy snippets in
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
// (capacity, extensions, runtime info) that the projection
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

// New constructs a VFS over view/fsops with the given routing
// configuration.
func New(view *projection.View, fsops *projection.FSOps, cfg projection.RoutingConfig, opts ...Option) *VFS {
	v := &VFS{
		view:       view,
		fsops:      fsops,
		routingCfg: cfg,
		startedAt:  time.Now().UTC(),
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// CleanPath normalises a slash-style path: drops the leading
// and trailing slash. The result is a tree-relative path
// suitable for projection.Route. Surfaces hand raw paths to
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
		return WrapErrno(errs.ErrEditingDisabled)
	}
	return WrapErr(v.fsops.Mkdir(clean, uint32(perm)))
}

// RemoveAll deletes a file or empty directory. Recursive
// deletion of non-empty trees is intentionally not
// implemented — surfaces that promise it (WebDAV's
// RemoveAll) translate the resulting ErrNotEmpty themselves.
func (v *VFS) RemoveAll(ctx context.Context, name string) error {
	clean := CleanPath(name)
	if clean == "" {
		// Refuse to remove the mount root.
		return WrapErrno(errs.ErrEditingDisabled)
	}
	if isAtServiceRoot(clean, v.routingCfg) {
		return WrapErrno(errs.ErrEditingDisabled)
	}
	// Try Unlink first (file). If the path is a directory,
	// fall back to Rmdir.
	err := v.fsops.Unlink(ctx, clean)
	if err == nil {
		return nil
	}
	if errors.Is(err, errs.ErrIsADirectory) {
		return WrapErr(v.fsops.Rmdir(clean))
	}
	return WrapErr(err)
}

// Rename moves a path. Cross-tree renames are not supported:
// destinations under the service prefix are rejected.
func (v *VFS) Rename(ctx context.Context, oldName, newName string) error {
	oldClean := CleanPath(oldName)
	newClean := CleanPath(newName)
	if isAtServiceRoot(newClean, v.routingCfg) {
		return WrapErrno(errs.ErrEditingDisabled)
	}
	return WrapErr(v.fsops.Rename(ctx, oldClean, newClean))
}

// --- read methods ---

// Stat returns metadata for a path. Routing is centralised in
// projection.Route; VFS just translates the route into a
// FileInfo. Service trees produce synthetic infos for the
// service root and the stats virtual file; the rest go
// through the View.
func (v *VFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	clean := CleanPath(name)
	target, err := projection.Route(clean, v.routingCfg)
	if err != nil {
		if errors.Is(err, projection.ErrRouteRejected) {
			return nil, fs.ErrNotExist
		}
		return nil, WrapErr(err)
	}
	switch target.Kind {
	case projection.RouteRoot:
		fi, err := v.fsops.Stat(target.SubPath)
		if err != nil {
			return nil, WrapErr(err)
		}
		return projectionFileInfo{fi: fi}, nil
	case projection.RouteServiceRoot:
		return synthDirInfo(v.routingCfg.ServicePrefix, v.startedAt), nil
	case projection.RouteServiceTree:
		node, err := serviceLookup(v.view, target.Tree, target.SubPath)
		if err != nil {
			return nil, WrapErr(err)
		}
		return projectionNodeInfo{node: node, fallbackTime: v.startedAt}, nil
	case projection.RouteStatsFile:
		body := v.statsBody()
		return synthFileInfo("stats", int64(len(body)), time.Now()), nil
	case projection.RouteRawMirror:
		return nil, fs.ErrNotExist // raw mirror not implemented yet
	}
	return nil, fs.ErrNotExist
}

// OpenFile opens a path. Flags follow POSIX: O_CREATE creates,
// O_TRUNC truncates, O_RDWR/O_WRONLY allow writes. Read-only
// callers ignore flag bits and pass 0.
func (v *VFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (File, error) {
	clean := CleanPath(name)
	target, err := projection.Route(clean, v.routingCfg)
	if err != nil {
		if errors.Is(err, projection.ErrRouteRejected) {
			return nil, fs.ErrNotExist
		}
		return nil, WrapErr(err)
	}

	wantCreate := flag&syscallOCreate != 0
	wantTrunc := flag&os.O_TRUNC != 0
	wantWrite := flag&(os.O_WRONLY|os.O_RDWR) != 0
	_ = wantTrunc

	switch target.Kind {
	case projection.RouteRoot:
		return v.openRoot(ctx, target.SubPath, flag, perm, wantCreate, wantWrite)

	case projection.RouteServiceRoot:
		if wantWrite || wantCreate {
			return nil, WrapErrno(errs.ErrEditingDisabled)
		}
		return newServiceDirFile(v, v.routingCfg.ServicePrefix, "", true), nil

	case projection.RouteServiceTree:
		if wantWrite || wantCreate {
			return nil, WrapErrno(errs.ErrEditingDisabled)
		}
		node, err := serviceLookup(v.view, target.Tree, target.SubPath)
		if err != nil {
			return nil, WrapErr(err)
		}
		if node.FS.IsDir {
			return newServiceDirFile(v, target.Tree, target.SubPath, false), nil
		}
		rh, err := serviceOpen(ctx, v.view, target.Tree, target.SubPath)
		if err != nil {
			return nil, WrapErr(err)
		}
		return &readHandleFile{
			rh:    rh,
			name:  pathx.LastSegment(target.SubPath),
			path:  target.SubPath,
			size:  node.FS.Size,
			mtime: nodeModTime(node, v.startedAt),
			isDir: false,
		}, nil

	case projection.RouteStatsFile:
		if wantWrite || wantCreate {
			return nil, WrapErrno(errs.ErrEditingDisabled)
		}
		body := v.statsBody()
		return newBytesFile("stats", body, time.Now()), nil

	case projection.RouteRawMirror:
		return nil, fs.ErrNotExist
	}
	return nil, fs.ErrNotExist
}

// View returns the underlying projection.View for surfaces
// that need direct access (HTML browsers querying related
// artifacts, search, locations). Don't use for plain Stat /
// OpenFile traffic — go through the VFS for that.
func (v *VFS) View() *projection.View { return v.view }

// FSOps returns the underlying FSOps. Same caveat as View.
func (v *VFS) FSOps() *projection.FSOps { return v.fsops }

// RoutingConfig returns the routing config the VFS was
// constructed with. Surfaces use it to format URLs that
// route into service trees.
func (v *VFS) RoutingConfig() projection.RoutingConfig { return v.routingCfg }

// StartedAt returns the boot timestamp captured at New time.
// Used by surfaces rendering uptime.
func (v *VFS) StartedAt() time.Time { return v.startedAt }

// --- helpers ---

// isAtServiceRoot reports whether a path is exactly the
// service prefix or anything under it. Service tree paths
// are read-only; mutating methods reject them.
func isAtServiceRoot(clean string, cfg projection.RoutingConfig) bool {
	if cfg.ServicePrefix == "" {
		return false
	}
	return pathx.IsUnder(clean, cfg.ServicePrefix)
}

// nodeModTime returns the View node's modification time, or
// fallback when the node has no time recorded.
func nodeModTime(n projection.Node, fallback time.Time) time.Time {
	if !n.FS.ModTime.IsZero() {
		return n.FS.ModTime
	}
	return fallback
}

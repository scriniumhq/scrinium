package vfs

import (
	"errors"

	"scrinium.dev/internal/pathx"
	"scrinium.dev/projection/internal/view"
)

// kind tags the destination of a routed path. The dispatcher
// branches on this value to choose between FSOps (for the root
// view) and direct View access (for the service trees), plus
// virtual files (stats) and the raw mirror.
type kind int

const (
	// kindRoot — the path lives in the configured root tree and
	// goes through FSOps (read-write per editing policy).
	kindRoot kind = iota

	// kindServiceTree — the path lives in a _scrinium/<treeName>
	// subtree, served read-only directly from the View.
	kindServiceTree

	// kindServiceRoot — the path is exactly the service-prefix
	// directory (e.g. "_scrinium"). The dispatcher exposes a
	// synthesised directory listing of the enabled service trees.
	kindServiceRoot

	// kindStatsFile — the path is _scrinium/stats; the dispatcher
	// returns a virtual file whose contents are generated on
	// each read.
	kindStatsFile

	// kindRawMirror — the path is under _scrinium/raw/. The
	// dispatcher serves it directly from the store directory on
	// disk (read-only).
	kindRawMirror

	// kindRejected — the path is reserved or otherwise refused
	// (e.g. the root component is a duplicate of the service
	// prefix and cannot be created).
	kindRejected
)

// target is the result of route. The fields meaningful for a
// given kind are listed in the kind doc above.
type target struct {
	Kind kind

	// Tree is the View tree to query when Kind == kindRoot or
	// kindServiceTree. Unused otherwise.
	Tree view.RootView

	// SubPath is the path *inside* Tree. For kindRoot it's the
	// input path verbatim; for kindServiceTree it's the input
	// path with the "_scrinium/<treeName>/" prefix stripped.
	// Empty string means "tree root".
	SubPath string

	// RawSubPath, when Kind == kindRawMirror, is the path inside
	// the store directory (the part after "_scrinium/raw/").
	RawSubPath string
}

// Config is the VFS namespace/presentation policy: where the
// service prefix lives, which diagnostic trees are visible, and
// whether they are exposed prefixed or at the root. Each surface
// (fuse, webdav, webview) constructs its own.
//
// Note there is deliberately no RootView field: the tree that
// backs the root is a property of the View (View.RootView()),
// and the VFS reads it from there so routing always agrees with
// FSOps, which resolves the root tree the same way.
type Config struct {
	// ServicePrefix is the root component reserved for service
	// trees. Empty disables the service surface entirely; every
	// path then routes to kindRoot.
	ServicePrefix string

	// Show* gate the individual diagnostic trees. A path under a
	// hidden service tree returns kindRejected (the dispatcher
	// then surfaces ENOENT).
	ShowStats       bool
	ShowByArtifact  bool
	ShowOrphaned    bool
	ShowByDate      bool
	ShowBySession   bool
	ShowByNamespace bool
	ShowRaw         bool

	// UnprefixedServiceTrees, when true, exposes service tree
	// names (by-path, by-date, by-session, by-namespace,
	// by-artifact, orphaned, stats, raw) at the root of the
	// path namespace — without the ServicePrefix wrapper.
	//
	// Only honoured when ServicePrefix is empty: the
	// configurations are mutually exclusive. Surfaces that
	// dedicate the entire URL space to diagnostics (webview)
	// turn this on; surfaces sharing root with user content
	// (webdav, fuse) keep ServicePrefix non-empty and leave
	// this off.
	//
	// Caveat: with UnprefixedServiceTrees on, names like
	// "by-date" cannot exist as ordinary path components
	// (they always route to the service tree). The webview
	// surface accepts this trade-off because it never
	// surfaces user content under the root anyway.
	UnprefixedServiceTrees bool
}

// errRejected is returned by route when the path falls into
// kindRejected. The dispatcher translates it to ENOENT or EACCES
// depending on the call site.
var errRejected = errors.New("scrinium-vfs: path rejected by routing")

// route classifies an incoming filesystem path. The path is
// slash-separated, no leading slash (consistent with the
// projection package's convention). An empty path is the mount
// root. rootView is the tree that backs kindRoot, derived by the
// caller from View.RootView().
//
// Routing rules:
//
//   - "" → kindRoot at rootView, SubPath="".
//   - "<servicePrefix>" → kindServiceRoot.
//   - "<servicePrefix>/<treeName>[/...]" → kindServiceTree at the
//     corresponding tree (or kindStatsFile, kindRawMirror for the
//     special leaves), provided the tree is enabled in cfg.
//     Disabled tree → kindRejected.
//   - everything else → kindRoot, SubPath = path.
//
// Service prefix in non-root positions is allowed: "photos/_scrinium"
// is a regular path component. Only the first segment matters.
//
// The function does no I/O and does not consult the View; it is
// pure with respect to its inputs.
func route(path string, cfg Config, rootView view.RootView) (target, error) {
	// Mount root.
	if path == "" {
		return target{
			Kind: kindRoot,
			Tree: rootView,
		}, nil
	}

	// Disabled service surface — every path is regular
	// unless UnprefixedServiceTrees flips into "service tree
	// names live at the root" mode.
	if cfg.ServicePrefix == "" {
		if cfg.UnprefixedServiceTrees {
			// Treat the first segment as a tree name.
			// dispatchServiceTree returns kindRoot when no
			// match, so plain user paths still work in the
			// rare case the host has no service trees enabled.
			return dispatchServiceTree(path, cfg, rootView)
		}
		return target{
			Kind:    kindRoot,
			Tree:    rootView,
			SubPath: path,
		}, nil
	}

	first, rest := pathx.SplitFirst(path)

	// Non-service first segment: regular root path.
	if first != cfg.ServicePrefix {
		return target{
			Kind:    kindRoot,
			Tree:    rootView,
			SubPath: path,
		}, nil
	}

	// Exactly the service prefix root.
	if rest == "" {
		return target{Kind: kindServiceRoot}, nil
	}

	// Inside the service prefix: dispatch by the second
	// segment via the same logic UnprefixedServiceTrees uses.
	return dispatchServiceTree(rest, cfg, rootView)
}

// dispatchServiceTree maps a path whose first segment is a
// tree name (by-path, by-date, etc.) to its target.
// Used both by the prefixed flow (after stripping
// ServicePrefix) and the unprefixed flow (as the top-level
// dispatcher when ServicePrefix is empty).
func dispatchServiceTree(path string, cfg Config, rootView view.RootView) (target, error) {
	tree, treeRest := pathx.SplitFirst(path)
	switch tree {
	case "stats":
		if !cfg.ShowStats {
			return target{Kind: kindRejected}, errRejected
		}
		// stats is a leaf file; sub-paths under it are nonsense.
		if treeRest != "" {
			return target{Kind: kindRejected}, errRejected
		}
		return target{Kind: kindStatsFile}, nil

	case "by-artifact":
		if !cfg.ShowByArtifact {
			return target{Kind: kindRejected}, errRejected
		}
		return target{
			Kind:    kindServiceTree,
			Tree:    view.RootByArtifact,
			SubPath: treeRest,
		}, nil

	case "orphaned":
		if !cfg.ShowOrphaned {
			return target{Kind: kindRejected}, errRejected
		}
		// orphaned has its own tree (RootByOrphaned), populated
		// only with artifacts whose path could not be resolved.
		// Distinct from by-artifact (which contains every
		// artifact). See projection/internal/view indexArtifact.
		return target{
			Kind:    kindServiceTree,
			Tree:    view.RootByOrphaned,
			SubPath: treeRest,
		}, nil

	case "by-date":
		if !cfg.ShowByDate {
			return target{Kind: kindRejected}, errRejected
		}
		return target{
			Kind:    kindServiceTree,
			Tree:    view.RootByDate,
			SubPath: treeRest,
		}, nil

	case "by-session":
		if !cfg.ShowBySession {
			return target{Kind: kindRejected}, errRejected
		}
		return target{
			Kind:    kindServiceTree,
			Tree:    view.RootBySession,
			SubPath: treeRest,
		}, nil

	case "by-namespace":
		if !cfg.ShowByNamespace {
			return target{Kind: kindRejected}, errRejected
		}
		return target{
			Kind:    kindServiceTree,
			Tree:    view.RootByNamespace,
			SubPath: treeRest,
		}, nil

	case "by-path":
		// Always available when servicePrefix is set; the dispatcher
		// surfaces this in case the user picked a non-by-path
		// RootView and wants the path tree as a service view.
		return target{
			Kind:    kindServiceTree,
			Tree:    view.RootByPath,
			SubPath: treeRest,
		}, nil

	case "raw":
		if !cfg.ShowRaw {
			return target{Kind: kindRejected}, errRejected
		}
		return target{
			Kind:       kindRawMirror,
			RawSubPath: treeRest,
		}, nil
	}

	// Unknown first segment.
	//
	// In prefixed mode (we got here after stripping
	// ServicePrefix) anything unknown under the prefix is a
	// nonsense path → kindRejected.
	//
	// In unprefixed mode the first segment is just a
	// regular path component, so the path routes to the
	// root view. This is what lets the empty-prefix surface
	// still serve user content even when service trees take
	// the same namespace.
	if cfg.ServicePrefix == "" && cfg.UnprefixedServiceTrees {
		return target{
			Kind:    kindRoot,
			Tree:    rootView,
			SubPath: path,
		}, nil
	}
	return target{Kind: kindRejected}, errRejected
}

// isServicePath reports whether the path's first segment equals
// the configured service prefix. Useful when validating new-file
// creation: writes to <servicePrefix>/* are forbidden because the
// service trees are read-only.
func isServicePath(path string, cfg Config) bool {
	if cfg.ServicePrefix == "" {
		return false
	}
	first, _ := pathx.SplitFirst(path)
	return first == cfg.ServicePrefix
}

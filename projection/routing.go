package projection

import (
	"errors"

	"scrinium.dev/internal/pathx"
)

// RouteKind tags the destination of a routed path. The FUSE
// dispatcher branches on this value to choose between FSOps
// (for the root view) and direct View access (for the service
// trees), plus virtual files (stats) and the raw mirror.
type RouteKind int

const (
	// RouteRoot — the path lives in the configured root tree and
	// goes through FSOps (read-write per editing policy).
	RouteRoot RouteKind = iota

	// RouteServiceTree — the path lives in a _scrinium/<treeName>
	// subtree, served read-only directly from the View.
	RouteServiceTree

	// RouteServiceRoot — the path is exactly the service-prefix
	// directory (e.g. "_scrinium"). The dispatcher exposes a
	// synthesised directory listing of the enabled service trees.
	RouteServiceRoot

	// RouteStatsFile — the path is _scrinium/stats; the dispatcher
	// returns a virtual file whose contents are generated on
	// each read.
	RouteStatsFile

	// RouteRawMirror — the path is under _scrinium/raw/. The
	// dispatcher serves it directly from the store directory on
	// disk (read-only).
	RouteRawMirror

	// RouteRejected — the path is reserved or otherwise refused
	// (e.g. the root component is a duplicate of the service
	// prefix and cannot be created).
	RouteRejected
)

// RouteTarget is the result of Route. The fields meaningful for a
// given Kind are listed in the Kind doc above.
type RouteTarget struct {
	Kind RouteKind

	// Tree is the View tree to query when Kind == RouteRoot or
	// RouteServiceTree. Unused otherwise.
	Tree RootView

	// SubPath is the path *inside* Tree. For RouteRoot it's the
	// input path verbatim; for RouteServiceTree it's the input
	// path with the "_scrinium/<treeName>/" prefix stripped.
	// Empty string means "tree root".
	SubPath string

	// RawSubPath, when Kind == RouteRawMirror, is the path inside
	// the store directory (the part after "_scrinium/raw/").
	RawSubPath string
}

// RoutingConfig captures the routing-relevant subset of Config.
// Decoupled so routing.go does not depend on the full Config
// definition (and tests can construct it cheaply).
type RoutingConfig struct {
	// ServicePrefix is the root component reserved for service
	// trees. Empty disables the service surface entirely; every
	// path then routes to RouteRoot.
	ServicePrefix string

	// RootView selects the tree that backs RouteRoot.
	RootView RootView

	// Show* mirror Config.Show* flags. A path under a hidden
	// service tree returns RouteRejected (the dispatcher then
	// surfaces ENOENT).
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

// ErrRouteRejected is returned by Route when the path falls into
// RouteRejected. The dispatcher translates it to ENOENT or EACCES
// depending on the call site.
var ErrRouteRejected = errors.New("scrinium-fuse: path rejected by routing")

// Route classifies an incoming filesystem path. The path is
// slash-separated, no leading slash (consistent with the
// projection package's convention). An empty path is the mount
// root.
//
// Routing rules:
//
//   - "" → RouteRoot at the configured RootView, SubPath="".
//   - "<servicePrefix>" → RouteServiceRoot.
//   - "<servicePrefix>/<treeName>[/...]" → RouteServiceTree at the
//     corresponding RootView (or RouteStatsFile, RouteRawMirror
//     for the special leaves), provided the tree is enabled in
//     cfg. Disabled tree → RouteRejected.
//   - everything else → RouteRoot, SubPath = path.
//
// Service prefix in non-root positions is allowed: "photos/_scrinium"
// is a regular path component. Only the first segment matters.
//
// The function does no I/O and does not consult the View; it is
// pure with respect to its inputs.
func Route(path string, cfg RoutingConfig) (RouteTarget, error) {
	// Mount root.
	if path == "" {
		return RouteTarget{
			Kind: RouteRoot,
			Tree: cfg.RootView,
		}, nil
	}

	// Disabled service surface — every path is regular
	// unless UnprefixedServiceTrees flips into "service tree
	// names live at the root" mode.
	if cfg.ServicePrefix == "" {
		if cfg.UnprefixedServiceTrees {
			// Treat the first segment as a tree name.
			// dispatchServiceTree returns RouteRoot when no
			// match, so plain user paths still work in the
			// rare case the host has no service trees enabled.
			return dispatchServiceTree(path, cfg)
		}
		return RouteTarget{
			Kind:    RouteRoot,
			Tree:    cfg.RootView,
			SubPath: path,
		}, nil
	}

	first, rest := pathx.SplitFirst(path)

	// Non-service first segment: regular root path.
	if first != cfg.ServicePrefix {
		return RouteTarget{
			Kind:    RouteRoot,
			Tree:    cfg.RootView,
			SubPath: path,
		}, nil
	}

	// Exactly the service prefix root.
	if rest == "" {
		return RouteTarget{Kind: RouteServiceRoot}, nil
	}

	// Inside the service prefix: dispatch by the second
	// segment via the same logic UnprefixedServiceTrees uses.
	return dispatchServiceTree(rest, cfg)
}

// dispatchServiceTree maps a path whose first segment is a
// tree name (by-path, by-date, etc.) to its RouteTarget.
// Used both by the prefixed flow (after stripping
// ServicePrefix) and the unprefixed flow (as the top-level
// dispatcher when ServicePrefix is empty).
func dispatchServiceTree(path string, cfg RoutingConfig) (RouteTarget, error) {
	tree, treeRest := pathx.SplitFirst(path)
	switch tree {
	case "stats":
		if !cfg.ShowStats {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		// stats is a leaf file; sub-paths under it are nonsense.
		if treeRest != "" {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		return RouteTarget{Kind: RouteStatsFile}, nil

	case "by-artifact":
		if !cfg.ShowByArtifact {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		return RouteTarget{
			Kind:    RouteServiceTree,
			Tree:    RootByArtifact,
			SubPath: treeRest,
		}, nil

	case "orphaned":
		if !cfg.ShowOrphaned {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		// orphaned has its own tree (RootByOrphaned), populated
		// only with artifacts whose path could not be resolved.
		// Distinct from by-artifact (which contains every
		// artifact). See projection/view.go indexArtifact.
		return RouteTarget{
			Kind:    RouteServiceTree,
			Tree:    RootByOrphaned,
			SubPath: treeRest,
		}, nil

	case "by-date":
		if !cfg.ShowByDate {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		return RouteTarget{
			Kind:    RouteServiceTree,
			Tree:    RootByDate,
			SubPath: treeRest,
		}, nil

	case "by-session":
		if !cfg.ShowBySession {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		return RouteTarget{
			Kind:    RouteServiceTree,
			Tree:    RootBySession,
			SubPath: treeRest,
		}, nil

	case "by-namespace":
		if !cfg.ShowByNamespace {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		return RouteTarget{
			Kind:    RouteServiceTree,
			Tree:    RootByNamespace,
			SubPath: treeRest,
		}, nil

	case "by-path":
		// Always available when servicePrefix is set; the dispatcher
		// surfaces this in case the user picked a non-by-path
		// RootView and wants the path tree as a service view.
		return RouteTarget{
			Kind:    RouteServiceTree,
			Tree:    RootByPath,
			SubPath: treeRest,
		}, nil

	case "raw":
		if !cfg.ShowRaw {
			return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
		}
		return RouteTarget{
			Kind:       RouteRawMirror,
			RawSubPath: treeRest,
		}, nil
	}

	// Unknown first segment.
	//
	// In prefixed mode (we got here after stripping
	// ServicePrefix) anything unknown under the prefix is a
	// nonsense path → RouteRejected.
	//
	// In unprefixed mode the first segment is just a
	// regular path component, so the path routes to the
	// root view. This is what lets the empty-prefix surface
	// still serve user content even when service trees take
	// the same namespace.
	if cfg.ServicePrefix == "" && cfg.UnprefixedServiceTrees {
		return RouteTarget{
			Kind:    RouteRoot,
			Tree:    cfg.RootView,
			SubPath: path,
		}, nil
	}
	return RouteTarget{Kind: RouteRejected}, ErrRouteRejected
}

// isServicePath reports whether the path's first segment equals
// the configured service prefix. Useful when validating new-file
// creation: writes to <servicePrefix>/* are forbidden because the
// service trees are read-only.
func isServicePath(path string, cfg RoutingConfig) bool {
	if cfg.ServicePrefix == "" {
		return false
	}
	first, _ := pathx.SplitFirst(path)
	return first == cfg.ServicePrefix
}

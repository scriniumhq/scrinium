package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"scrinium.dev/projection/node"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/projection"
	"scrinium.dev/projection/fsindex"
	"scrinium.dev/projection/fsmeta"
)

// buildView constructs the read-side projection. The projection
// section (when present) selects the root tree and the by-path
// fallback; otherwise engine defaults stand.
func buildView(
	ctx context.Context,
	st store.Store,
	fsidx *fsindex.Extension,
	p *Projection,
) (*projection.View, error) {
	opts := []projection.ViewOption{
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFSIndex(fsidx),
	}
	if p != nil {
		if p.RootView != "" {
			opts = append(opts, projection.WithRootView(node.RootView(p.RootView)))
		}
		if p.ByPathFallback != "" {
			opts = append(opts, projection.WithFallback(projection.PathFallback(p.ByPathFallback)))
		}
	}
	view, err := projection.NewView(ctx, st, opts...)
	if err != nil {
		return nil, fmt.Errorf("build view: %w", err)
	}
	return view, nil
}

// buildFSOps constructs the read/write facade from the projection
// section, mirroring the legacy FSOps wiring. storeURI is used to
// default the scratch directory under a local store. uid/gid default
// to the running process when left at zero (matching the old
// the historical default behaviour).
func buildFSOps(
	view *projection.View,
	st store.Store,
	p *Projection,
	mountSession domain.SessionID,
	storeURI string,
) (*projection.FSOps, error) {
	opts := []projection.FSOpsOption{
		projection.WithStore(st),
		projection.WithMountSession(mountSession),
		projection.WithScratchQuota(p.ScratchQuota.Int64()),
		projection.WithDefaultMode(p.DefaultMode),
		projection.WithDefaultUID(defaultID(p.DefaultUID, os.Getuid)),
		projection.WithDefaultGID(defaultID(p.DefaultGID, os.Getgid)),
		projection.WithEditingPolicy(editingPolicy(p)),
		projection.WithNamespace(p.Namespace),
	}

	if p.ReadOnly {
		opts = append(opts, projection.WithReadOnly())
	} else {
		scratch, err := resolveScratchDir(p.ScratchDir, storeURI)
		if err != nil {
			return nil, err
		}
		if scratch != "" {
			opts = append(opts, projection.WithScratchDir(scratch))
		}
	}

	fsops, err := projection.NewFSOps(view, opts...)
	if err != nil {
		return nil, fmt.Errorf("build fsops: %w", err)
	}
	return fsops, nil
}

// editingPolicy maps the config string onto a projection.EditingPolicy.
// "custom" consults the Allow* pointer flags (nil = false).
func editingPolicy(p *Projection) projection.EditingPolicy {
	switch p.Editing {
	case "on":
		return projection.EditingOn()
	case "custom":
		return projection.EditingPolicy{
			AllowRename:   derefBool(p.AllowRename),
			AllowSetattr:  derefBool(p.AllowSetattr),
			AllowTruncate: derefBool(p.AllowTruncate),
			AllowAppend:   derefBool(p.AllowAppend),
		}
	default:
		return projection.EditingOff()
	}
}

// resolveScratchDir returns the configured scratch path, or a default
// under a local store directory. Empty (no default possible) for a
// non-local store with no explicit path — FSOps then runs without a
// scratch dir, which the engine tolerates for read-mostly use.
func resolveScratchDir(configured, storeURI string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	p, err := localStorePath(storeURI)
	if err != nil {
		// Non-local store: no sensible default. Leave empty rather
		// than failing — an explicit scratchDir is required only when
		// the adapter actually performs writes.
		return "", nil
	}
	return filepath.Join(p, ".scratch"), nil
}

func defaultID(v uint32, fallback func() int) uint32 {
	if v != 0 {
		return v
	}
	id := fallback()
	if id < 0 {
		return 0
	}
	return uint32(id)
}

func derefBool(p *bool) bool { return p != nil && *p }

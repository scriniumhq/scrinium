package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	fso "scrinium.dev/projection/fsops"
	"scrinium.dev/projection/node"
	vw "scrinium.dev/projection/view"

	"scrinium.dev/domain"
	"scrinium.dev/domain/fsmeta"
	"scrinium.dev/engine/index/fsindex"
	"scrinium.dev/engine/store"
)

// buildView constructs the read-side projection. The projection
// section (when present) selects the root tree and the by-path
// fallback; otherwise engine defaults stand.
func buildView(
	ctx context.Context,
	st store.Store,
	fsidx *fsindex.Extension,
	p *Projection,
) (*vw.View, error) {
	opts := []vw.Option{
		vw.WithPathResolver(fsmeta.Resolver),
		vw.WithFSIndex(fsidx),
	}
	if p != nil {
		if p.RootView != "" {
			opts = append(opts, vw.WithRootView(node.RootView(p.RootView)))
		}
		if p.ByPathFallback != "" {
			opts = append(opts, vw.WithFallback(vw.Fallback(p.ByPathFallback)))
		}
	}
	view, err := vw.New(ctx, st, opts...)
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
	view *vw.View,
	st store.Store,
	p *Projection,
	mountSession domain.SessionID,
	storeURI string,
) (*fso.Ops, error) {
	opts := []fso.Option{
		fso.WithStore(st),
		fso.WithMountSession(mountSession),
		fso.WithScratchQuota(p.ScratchQuota.Int64()),
		fso.WithDefaultMode(p.DefaultMode),
		fso.WithDefaultUID(defaultID(p.DefaultUID, os.Getuid)),
		fso.WithDefaultGID(defaultID(p.DefaultGID, os.Getgid)),
		fso.WithEditingPolicy(editingPolicy(p)),
		fso.WithNamespace(p.Namespace),
	}

	if p.ReadOnly {
		opts = append(opts, fso.WithReadOnly())
	} else {
		scratch, err := resolveScratchDir(p.ScratchDir, storeURI)
		if err != nil {
			return nil, err
		}
		if scratch != "" {
			opts = append(opts, fso.WithScratchDir(scratch))
		}
	}

	fsops, err := fso.New(view, opts...)
	if err != nil {
		return nil, fmt.Errorf("build fsops: %w", err)
	}
	return fsops, nil
}

// editingPolicy maps the config string onto a fso.EditingPolicy.
// "custom" consults the Allow* pointer flags (nil = false).
func editingPolicy(p *Projection) fso.EditingPolicy {
	switch p.Editing {
	case "on":
		return fso.EditingOn()
	case "custom":
		return fso.EditingPolicy{
			AllowRename:   derefBool(p.AllowRename),
			AllowSetattr:  derefBool(p.AllowSetattr),
			AllowTruncate: derefBool(p.AllowTruncate),
			AllowAppend:   derefBool(p.AllowAppend),
		}
	default:
		return fso.EditingOff()
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

package projection

import (
	"context"
	"fmt"
	"os"

	"scrinium.dev/domain/fsmeta"
	"scrinium.dev/projection/internal/fsops"
	"scrinium.dev/projection/internal/source"
	"scrinium.dev/projection/internal/view"
)

// MetadataIndex is the read-side contract the View consults for fast
// ext/path lookups. It is satisfied by engine/index/fsindex's
// registered custom index. The composition root registers the custom index
// with the store's index backend (which must happen before the store
// opens) and then hands the same handle to Build.
type MetadataIndex = source.Metadata

// Backend is the store surface a projection needs: the read side the
// View walks and fetches from, plus the write side FSOps uses. It is
// the union of the internal source.Provider and fsops.StoreClient
// contracts. A *store.Store (or any value with these methods, e.g. a
// test fake) satisfies it structurally, so Build's public signature
// names no concrete store type.
type Backend interface {
	source.Provider
	fsops.StoreClient
}

// Build wires the read-side View and the read/write FSOps facade over
// a store backend, per cfg. The backend must already have the fsindex
// custom index registered and fsidx must be that custom index's handle. The
// returned Projection owns the View; Close releases it.
func Build(ctx context.Context, backend Backend, fsidx MetadataIndex, cfg Config) (*Projection, error) {
	v, err := buildView(ctx, backend, fsidx, cfg)
	if err != nil {
		return nil, err
	}
	ops, err := buildFSOps(v, backend, cfg)
	if err != nil {
		_ = v.Close()
		return nil, err
	}
	return &Projection{View: v, FSOps: ops}, nil
}

// buildView constructs the read-side projection. cfg selects the root
// tree and the by-path fallback; zero values leave engine defaults.
func buildView(ctx context.Context, backend Backend, fsidx MetadataIndex, cfg Config) (*view.View, error) {
	opts := []view.Option{
		view.WithPathResolver(fsmeta.Resolver),
		view.WithFSIndex(fsidx),
	}
	if cfg.RootView != "" {
		opts = append(opts, view.WithRootView(view.RootView(cfg.RootView)))
	}
	if cfg.ByPathFallback != "" {
		opts = append(opts, view.WithFallback(view.Fallback(cfg.ByPathFallback)))
	}
	v, err := view.New(ctx, backend, opts...)
	if err != nil {
		return nil, fmt.Errorf("build view: %w", err)
	}
	return v, nil
}

// buildFSOps constructs the read/write facade from cfg. uid/gid
// default to the running process when left at zero.
func buildFSOps(v *view.View, backend Backend, cfg Config) (*fsops.Ops, error) {
	opts := []fsops.Option{
		fsops.WithStore(backend),
		fsops.WithMountSession(cfg.MountSession),
		fsops.WithScratchQuota(cfg.ScratchQuota),
		fsops.WithDefaultMode(cfg.DefaultMode),
		fsops.WithDefaultUID(defaultID(cfg.DefaultUID, os.Getuid)),
		fsops.WithDefaultGID(defaultID(cfg.DefaultGID, os.Getgid)),
		fsops.WithEditingPolicy(editingPolicy(cfg)),
		fsops.WithNamespace(cfg.Namespace),
	}
	if cfg.ReadOnly {
		opts = append(opts, fsops.WithReadOnly())
	} else if cfg.ScratchDir != "" {
		opts = append(opts, fsops.WithScratchDir(cfg.ScratchDir))
	}
	ops, err := fsops.New(v, opts...)
	if err != nil {
		return nil, fmt.Errorf("build fsops: %w", err)
	}
	return ops, nil
}

// editingPolicy maps the config string onto a fsops.EditingPolicy.
// "custom" consults the Allow* pointer flags (nil = false).
func editingPolicy(cfg Config) fsops.EditingPolicy {
	switch cfg.Editing {
	case "on":
		return fsops.EditingOn()
	case "custom":
		return fsops.EditingPolicy{
			AllowRename:   derefBool(cfg.AllowRename),
			AllowSetattr:  derefBool(cfg.AllowSetattr),
			AllowTruncate: derefBool(cfg.AllowTruncate),
			AllowAppend:   derefBool(cfg.AllowAppend),
		}
	default:
		return fsops.EditingOff()
	}
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

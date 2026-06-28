package assembly

import (
	"context"
	"path/filepath"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/internal/uri"
	"scrinium.dev/projection"
)

// projectionConfig maps the assembly Projection config plus runtime
// inputs (mount session, store URI for the scratch default) onto a
// projection.Config. The actual View+FSOps wiring lives in
// projection.Build; assembly only owns config translation and the
// store-URI-aware scratch-dir default.
func projectionConfig(p *Projection, mountSession domain.SessionID, storeURI string) (projection.Config, error) {
	cfg := projection.Config{
		RootView:       p.RootView,
		ByPathFallback: p.ByPathFallback,
		Editing:        p.Editing,
		AllowRename:    p.AllowRename,
		AllowSetattr:   p.AllowSetattr,
		AllowTruncate:  p.AllowTruncate,
		AllowAppend:    p.AllowAppend,
		ScratchQuota:   p.ScratchQuota.Int64(),
		ReadOnly:       p.ReadOnly,
		DefaultMode:    p.DefaultMode,
		DefaultUID:     p.DefaultUID,
		DefaultGID:     p.DefaultGID,
		MountSession:   mountSession,
	}
	if !p.ReadOnly {
		scratch, err := resolveScratchDir(p.ScratchDir, storeURI)
		if err != nil {
			return projection.Config{}, err
		}
		cfg.ScratchDir = scratch
	}
	return cfg, nil
}

// resolveScratchDir returns the configured scratch path, or a default
// under a local store directory. Empty (no default possible) for a
// non-local store with no explicit path — FSOps then runs without a
// scratch dir, which the engine tolerates for read-mostly use.
func resolveScratchDir(configured, storeURI string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	p, err := uri.ResolveLocalURI(storeURI)
	if err != nil {
		// Non-local store: no sensible default. Leave empty rather
		// than failing — an explicit scratchDir is required only when
		// the adapter actually performs writes.
		return "", nil
	}
	return filepath.Join(p, ".scratch"), nil
}

// syncTokenSource adapts the index's SyncSource capability onto the
// projection's TokenSource (ADR-107). The projection takes no dependency on
// engine/index; the conversion between the index's typed Token and the
// projection's uint64 alias lives here, at the composition root.
type syncTokenSource struct{ ss index.SyncSource }

func (a syncTokenSource) Token(ctx context.Context) (uint64, error) {
	t, err := a.ss.Token(ctx)
	return uint64(t), err
}

// syncDeltaSource extends syncTokenSource with the incremental Since pull
// (ADR-107): it pairs the index's digest-level Since with its ManifestResolver
// to hand the projection resolved manifests, so a stale read upserts just the
// changes instead of re-walking. Installed only when the index implements both
// SyncSource and ManifestResolver; otherwise the plain syncTokenSource is used
// and the View re-derives fully. Satisfies projection.DeltaSource.
type syncDeltaSource struct {
	ss  index.SyncSource
	res index.ManifestResolver
}

func (a syncDeltaSource) Token(ctx context.Context) (uint64, error) {
	t, err := a.ss.Token(ctx)
	return uint64(t), err
}

func (a syncDeltaSource) Since(ctx context.Context, cursor uint64) (projection.Delta, error) {
	d, err := a.ss.Since(ctx, index.Token(cursor))
	if err != nil {
		return projection.Delta{}, err
	}
	out := projection.Delta{Next: uint64(d.Next), Gapped: d.Gapped}
	if d.Gapped {
		// The consumer will re-walk; resolving the changes would be wasted I/O.
		return out, nil
	}
	out.Changes = make([]domain.Manifest, 0, len(d.Changes))
	for _, c := range d.Changes {
		m, ok, rerr := a.res.ManifestByDigest(ctx, c.Digest)
		if rerr != nil {
			return projection.Delta{}, rerr
		}
		if ok {
			out.Changes = append(out.Changes, m)
		}
	}
	return out, nil
}

// syncWaiter adapts the index's SyncWaiter capability onto the projection's
// Waiter, so the view's eager watcher can block on backend changes.
type syncWaiter struct{ sw index.SyncWaiter }

func (a syncWaiter) Wait(ctx context.Context, after uint64) (uint64, error) {
	t, err := a.sw.Wait(ctx, index.Token(after))
	return uint64(t), err
}

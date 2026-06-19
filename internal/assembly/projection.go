package assembly

import (
	"path/filepath"

	"scrinium.dev/domain"
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

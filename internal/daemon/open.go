package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/index"
	"github.com/rkurbatov/scrinium/projection"
	"github.com/rkurbatov/scrinium/projection/fsindex"
	"github.com/rkurbatov/scrinium/projection/fsmeta"

	// Side-effect imports register the URI dialers. Adding new
	// schemes is a matter of importing the relevant packages
	// here. cmd binaries that want to constrain the available
	// schemes can import this package or the dialers
	// individually as needed.
	_ "github.com/rkurbatov/scrinium/driver/localfs"
	_ "github.com/rkurbatov/scrinium/index/sqlite"
)

// Open builds a Daemon from a Config: parses URIs, opens
// store and index, registers fsindex, builds the View,
// generates the mount session, prepares the scratch
// directory, and assembles FSOps.
//
// The order of operations matches what existing cmd binaries
// do today, just consolidated:
//
//  1. Validate config.
//  2. Open driver via DialDriver(cfg.Store).
//  3. Open index via DialIndex(cfg.Index)  — defaulted from
//     Store path when cfg.Index is empty and Store is file://.
//  4. Register fsindex extension. Must precede OpenStore so
//     the very first IndexManifest call dispatches into it.
//  5. core.OpenStore with hash registry.
//  6. Build View with fsmeta resolver and fsindex backing.
//  7. Generate MountSession.
//  8. Prepare scratch directory.
//  9. Build FSOps with policy from Config.
//
// On any failure the partial state is unwound: opened
// resources are closed, errors are wrapped with their stage
// for fast diagnosis.
func Open(ctx context.Context, cfg Config) (*Daemon, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// 1. Open driver from URI.
	drv, err := driver.DialDriver(cfg.Store)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}

	// 2. Resolve index URI (default if empty + local store).
	indexURI, err := resolveIndexURI(cfg)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}

	// 3. Open index.
	idx, err := index.DialIndex(ctx, indexURI)
	if err != nil {
		// Driver doesn't have a Close in our abstraction;
		// the localfs driver releases resources via its
		// internal handle close on store/index close, so we
		// just bubble up.
		return nil, fmt.Errorf("daemon: open index: %w", err)
	}

	// 4. Register fsindex. Must happen before OpenStore so
	//    the first IndexManifest dispatch already sees it.
	//
	//    Extension registration is a backend-specific feature
	//    — sqlite supports it, postgres will, but the
	//    abstract core.StoreIndex interface doesn't surface
	//    it (lifting it requires defining what registries
	//    mean across all future backends). We probe via
	//    type-assertion: backends that support it implement
	//    indexWithExtensions, others quietly skip.
	//
	//    Skipping has consequences — fsindex backfill no
	//    longer accelerates View construction, falling back
	//    to per-manifest Source.Get. That's correct behaviour
	//    on a backend without extension support; we'll
	//    surface a clearer story when such a backend exists.
	fsidx := fsindex.New()
	if extIdx, ok := idx.(indexWithExtensions); ok {
		if err := extIdx.Extensions().Register(ctx, fsidx); err != nil {
			_ = idx.Close()
			return nil, fmt.Errorf("daemon: register fsindex: %w", err)
		}
	}

	// 5. core.OpenStore wires driver + index + hash registry
	//    into a Store. Hash registry is fixed at sha256 here
	//    — every shipped binary uses the same; pluggable
	//    when we have a use case.
	store, err := core.OpenStore(ctx, drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(defaultHashRegistry()),
	)
	if err != nil {
		_ = idx.Close()
		return nil, fmt.Errorf("daemon: open store: %w", err)
	}

	// 6. Build View — synchronous backfill over all manifests.
	viewOpts := []projection.ViewOption{
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFSIndex(fsidx),
	}
	if cfg.RootView != "" {
		viewOpts = append(viewOpts, projection.WithRootView(cfg.RootView))
	}
	if cfg.ByPathFallback != "" {
		viewOpts = append(viewOpts, projection.WithFallback(projection.PathFallback(cfg.ByPathFallback)))
	}
	view, err := projection.NewView(ctx, store, viewOpts...)
	if err != nil {
		_ = idx.Close()
		return nil, fmt.Errorf("daemon: build view: %w", err)
	}

	// 7. Mount session — boot-unique tiebreaker.
	mountSession := "mount-" + uuid.New().String()

	// 8. Scratch directory.
	scratchDir, err := resolveScratchDir(cfg)
	if err != nil {
		view.Close()
		_ = idx.Close()
		return nil, fmt.Errorf("daemon: %w", err)
	}
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		view.Close()
		_ = idx.Close()
		return nil, fmt.Errorf("daemon: mkdir scratch: %w", err)
	}
	clearScratchDir(scratchDir)

	// 9. FSOps.
	fsopsOpts := []projection.FSOpsOption{
		projection.WithStore(store),
		projection.WithScratchDir(scratchDir),
		projection.WithScratchQuota(cfg.ScratchQuota),
		projection.WithDefaultMode(cfg.DefaultMode),
		projection.WithDefaultUID(cfg.DefaultUID),
		projection.WithDefaultGID(cfg.DefaultGID),
		projection.WithEditingPolicy(cfg.editingPolicy()),
		projection.WithMountSession(mountSession),
		projection.WithNamespace(cfg.Namespace),
	}
	if cfg.ReadOnly {
		fsopsOpts = append(fsopsOpts, projection.WithReadOnly())
	}
	fsops, err := projection.NewFSOps(view, fsopsOpts...)
	if err != nil {
		view.Close()
		_ = idx.Close()
		return nil, fmt.Errorf("daemon: build fsops: %w", err)
	}

	return &Daemon{
		Config:       cfg,
		Store:        store,
		Index:        idx,
		View:         view,
		FSOps:        fsops,
		FSIndex:      fsidx,
		MountSession: mountSession,
	}, nil
}

// resolveIndexURI returns cfg.Index when set, or synthesises a
// sqlite:// URI under the store directory when the store is
// file:// (the only scheme where a "next to the data" default
// makes sense). Other schemes require an explicit Index.
func resolveIndexURI(cfg Config) (string, error) {
	if cfg.Index != "" {
		return cfg.Index, nil
	}

	// Try to locate the store path. Two forms accepted:
	//   - bare path "/abs/path"  → local
	//   - file:///abs/path       → local
	// Anything else: error, because we don't know where to put
	// the default index.
	storePath := ""
	if u, err := url.Parse(cfg.Store); err == nil && u.Scheme == "file" {
		switch u.Host {
		case "":
			storePath = u.Path
		case "~":
			home, herr := os.UserHomeDir()
			if herr != nil {
				return "", fmt.Errorf("expand ~ in store URI: %w", herr)
			}
			storePath = filepath.Join(home, strings.TrimPrefix(u.Path, "/"))
		case ".":
			storePath = "." + u.Path
		}
	} else if !looksLikeSchemeURI(cfg.Store) {
		// Bare path (no scheme).
		storePath = cfg.Store
	}

	if storePath == "" {
		return "", fmt.Errorf("index URI is empty and cannot default for store %q (set index explicitly)", cfg.Store)
	}
	abs, err := filepath.Abs(expandTilde(storePath))
	if err != nil {
		return "", fmt.Errorf("resolve store path: %w", err)
	}
	return "sqlite:///" + filepath.Join(abs, "index.db"), nil
}

// resolveScratchDir returns cfg.ScratchDir, or a default under
// the store directory when the store is local.
func resolveScratchDir(cfg Config) (string, error) {
	if cfg.ScratchDir != "" {
		return cfg.ScratchDir, nil
	}
	storePath, err := localStorePath(cfg.Store)
	if err != nil {
		return "", fmt.Errorf("scratch dir unset and cannot default: %w", err)
	}
	return filepath.Join(storePath, ".scratch"), nil
}

// localStorePath extracts the on-disk path from a file:// URI
// or bare path. Errors when the store is non-local — callers
// know the default is unavailable and must require explicit
// configuration.
func localStorePath(storeURI string) (string, error) {
	if !looksLikeSchemeURI(storeURI) {
		return filepath.Abs(expandTilde(storeURI))
	}
	u, err := url.Parse(storeURI)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("non-local store scheme %q", u.Scheme)
	}
	switch u.Host {
	case "":
		return filepath.Abs(u.Path)
	case "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Abs(filepath.Join(home, strings.TrimPrefix(u.Path, "/")))
	case ".":
		return filepath.Abs("." + u.Path)
	}
	return "", fmt.Errorf("unsupported file:// host %q", u.Host)
}

// looksLikeSchemeURI is a thin wrapper to avoid duplicating
// the internal helper from driver/dial.go. We use a quick
// regex-equivalent check: scheme followed by "://".
func looksLikeSchemeURI(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := s[j]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '+', c == '-', c == '.':
			continue
		default:
			return false
		}
	}
	return true
}

// expandTilde mirrors driver.expandPath but leaves the absolute
// resolution to the caller — used in places where filepath.Abs
// happens later anyway.
func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// clearScratchDir wipes any leftover files from a previous
// crashed run. Best-effort: a failure here doesn't block boot.
func clearScratchDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// defaultHashRegistry returns a HashRegistry with sha256
// registered. Every Scrinium binary uses sha256 today; pluggable
// when an actual second hash arrives.
func defaultHashRegistry() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

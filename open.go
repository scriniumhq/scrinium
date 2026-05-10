package scrinium

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/projection/fsindex"
	"scrinium.dev/engine/projection/fsmeta"

	// Side-effect imports register the URI dialers. Adding new
	// schemes is a matter of importing the relevant packages
	// here. Hosts that want to constrain the available schemes
	// import the dialers individually.
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
)

// Open builds a Scrinium runtime from a Config: parses URIs,
// opens store and index, registers fsindex, builds the View,
// generates the mount session, prepares the scratch directory,
// and assembles FSOps.
//
// The order of operations:
//
//  1. Validate config.
//  2. Open driver via DialDriver(cfg.Store).
//  3. Resolve and open index (default sqlite under store dir
//     when cfg.Index is empty and store is file://).
//  4. Register fsindex extension. Must precede OpenStore so
//     the very first IndexManifest call dispatches into it.
//  5. core.OpenStore with hash registry. If
//     cfg.PassphraseFile is set, the store is opened with a
//     passphrase provider that reads the file; this transitions
//     an encrypted Store from Locked to Unlocked.
//  6. Build View with fsmeta resolver and fsindex backing.
//  7. Generate MountSession.
//  8. Prepare scratch directory.
//  9. Build FSOps with policy from Config.
//
// On any failure the partial state is unwound: opened resources
// are closed, errors are wrapped with their stage for fast
// diagnosis. Cleanup errors are logged to stderr; they cannot
// be returned alongside the primary error.
func Open(ctx context.Context, cfg Config) (_ *Scrinium, retErr error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Deferred cleanup of resources opened during this function.
	// Each successfully-opened resource appends its closer here;
	// on error the deferred function runs them in LIFO order.
	// On success retErr stays nil and the slice is cleared so
	// nothing is closed.
	var cleanups []func()
	defer func() {
		if retErr == nil {
			return
		}
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	// 1. Open driver from URI.
	drv, err := driver.DialDriver(cfg.Store)
	if err != nil {
		return nil, fmt.Errorf("scrinium.Open: %w", err)
	}

	// 2. Resolve index URI (default if empty + local store).
	indexURI, err := resolveIndexURI(cfg)
	if err != nil {
		return nil, fmt.Errorf("scrinium.Open: %w", err)
	}

	// 3. Open index.
	idx, err := index.DialIndex(ctx, indexURI)
	if err != nil {
		return nil, fmt.Errorf("scrinium.Open: open index: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := idx.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium.Open: index close on rollback: %v\n", err)
		}
	})

	//
	//    Extension registration is a backend-specific feature
	//    — sqlite supports it, postgres will, but the
	//    abstract core.StoreIndex interface doesn't surface
	//    it (lifting it requires defining what registries
	//    mean across all future backends). We probe via
	//    type-assertion: backends that support it implement
	//    index.ExtensionHost, others quietly skip.
	//
	//    Skipping has consequences — fsindex backfill no
	//    longer accelerates View construction, falling back
	//    to per-manifest Source.Get. That's correct behaviour
	//    on a backend without extension support; we'll
	//    surface a clearer story when such a backend exists.
	fsidx := fsindex.New()
	if extIdx, ok := idx.(index.ExtensionHost); ok {
		if err := extIdx.Extensions().Register(ctx, fsidx); err != nil {
			return nil, fmt.Errorf("scrinium.Open: register fsindex: %w", err)
		}
	}

	// 5. core.OpenStore wires driver + index + hash registry
	//    into a Store. Hash registry is fixed at sha256 here
	//    — every shipped binary uses the same; pluggable
	//    when we have a use case.
	//
	//    PassphraseFile (if set) flips the Store from a
	//    Plain-DEK open to an encrypted Unlock by attaching
	//    a passphrase provider. The provider re-reads the
	//    file each time it is invoked so RotateKEK / re-Unlock
	//    paths work correctly even if the host rotates the
	//    file in between.
	pp, err := loadPassphraseProvider(cfg.PassphraseFile)
	if err != nil {
		return nil, fmt.Errorf("scrinium.Open: %w", err)
	}
	storeOpts := []core.StoreOption{
		core.WithStoreIndex(idx),
		core.WithHashRegistry(defaultHashRegistry()),
	}
	if pp != nil {
		storeOpts = append(storeOpts, core.WithPassphrase(pp))
	}
	store, err := core.OpenStore(ctx, drv, storeOpts...)
	if err != nil {
		return nil, fmt.Errorf("scrinium.Open: open store: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium.Open: store close on rollback: %v\n", err)
		}
	})

	// If the store opened in StateLocked (encrypted, no
	// passphrase or wrong passphrase), surface that as an
	// error: callers expect an Unlocked Store from Open.
	// This catches the case "PassphraseFile points at the
	// wrong content" without burying it three layers deep.
	if store.State() == domain.StateLocked {
		return nil, fmt.Errorf("scrinium.Open: store is locked; check PassphraseFile")
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
		return nil, fmt.Errorf("scrinium.Open: build view: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := view.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "scrinium.Open: view close on rollback: %v\n", err)
		}
	})

	// 7. Mount session — boot-unique tiebreaker.
	mountSession := domain.NewMountSessionID()

	// 8. Scratch directory.
	//
	// Scratch is only used by FSOps writes (Create, Truncate,
	// Setattr, Rename when the editing policy permits them).
	// It is NOT used by Store.Put, Store.Get, or any read-side
	// operation. We pass the configured (or defaulted) path
	// down to FSOps but do NOT pre-create the directory:
	// projection.NewFSOps lazy-creates it at first write via
	// MkdirAll. This keeps Open side-effect-free for read-only
	// hosts and embedded use cases that never touch FSOps.
	//
	// We do best-effort wipe leftover scratch from a previous
	// crashed run, but ONLY if the directory already exists —
	// no MkdirAll, no inadvertent write to a parent that should
	// stay untouched.
	var scratchDir string
	if !cfg.ReadOnly {
		scratchDir, err = resolveScratchDir(cfg)
		if err != nil {
			return nil, fmt.Errorf("scrinium.Open: %w", err)
		}
		clearScratchDirIfExists(scratchDir)
	}

	// 9. FSOps.
	fsopsOpts := []projection.FSOpsOption{
		projection.WithStore(store),
		projection.WithScratchQuota(cfg.ScratchQuota),
		projection.WithDefaultMode(cfg.DefaultMode),
		projection.WithDefaultUID(cfg.DefaultUID),
		projection.WithDefaultGID(cfg.DefaultGID),
		projection.WithEditingPolicy(cfg.editingPolicy()),
		projection.WithMountSession(mountSession),
		projection.WithNamespace(cfg.Namespace),
	}
	if scratchDir != "" {
		fsopsOpts = append(fsopsOpts, projection.WithScratchDir(scratchDir))
	}
	if cfg.ReadOnly {
		fsopsOpts = append(fsopsOpts, projection.WithReadOnly())
	}
	fsops, err := projection.NewFSOps(view, fsopsOpts...)
	if err != nil {
		return nil, fmt.Errorf("scrinium.Open: build fsops: %w", err)
	}

	return &Scrinium{
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

	storePath, err := localStorePath(cfg.Store)
	if err != nil {
		return "", fmt.Errorf("index URI is empty and cannot default for store %q (set index explicitly)", cfg.Store)
	}
	return "sqlite:///" + filepath.Join(storePath, "index.db"), nil
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

// clearScratchDirIfExists wipes leftover files from a previous
// crashed run. Best-effort: if the directory does not exist,
// or any individual remove fails, we silently move on. We do
// NOT MkdirAll here — that's the job of projection.FSOps when
// it actually writes its first scratch file.
func clearScratchDirIfExists(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory does not exist or is unreadable — nothing
		// to clean. ReadDir on a missing path returns
		// fs.ErrNotExist; we treat all errors the same.
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

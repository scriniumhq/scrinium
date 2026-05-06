package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/webdav"

	"github.com/rkurbatov/scrinium/cmd/scrinium-webdav/web"
	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver/localfs"
	"github.com/rkurbatov/scrinium/index/sqlite"
	"github.com/rkurbatov/scrinium/projection"
	"github.com/rkurbatov/scrinium/projection/fsindex"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// runServe is the entry point for "scrinium-webdav serve". It
// builds the View, the FSOps, generates a mount session, wraps
// FSOps in a webdav.FileSystem adapter, and starts the HTTP
// listener.
//
// Lifecycle:
//
//  1. Parse + validate config.
//  2. Open the Store (localfs driver + sqlite index — referential
//     daemon, hosts wanting other backends write their own).
//  3. Build the View (synchronous backfill).
//  4. Build the FSOps with the configured policy.
//  5. Wrap as webdav.FileSystem and start http.Server.
//  6. Block on the server, propagating SIGINT/SIGTERM as a
//     graceful shutdown.
func runServe(args []string) int {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav serve: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	drv, err := localfs.New(cfg.StorePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: localfs driver: %v\n", err)
		return 1
	}
	idx, err := sqlite.NewStore(ctx, filepath.Join(cfg.StorePath, "index.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: sqlite index: %v\n", err)
		return 1
	}

	// Register the filesystem-projection index extension. fsindex
	// persists each artifact's fsmeta payload alongside the main
	// index in the same transaction, so View backfill can fetch
	// metadata in bulk instead of round-tripping Source.Get for
	// every manifest. Registration must happen before OpenStore
	// so the very first IndexManifest dispatches into fsindex.
	fsidx := fsindex.New()
	if err := idx.Extensions().Register(ctx, fsidx); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: register fsindex: %v\n", err)
		return 1
	}

	store, err := core.OpenStore(ctx, drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(defaultHashRegistry()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: open store: %v\n", err)
		return 1
	}

	view, err := projection.NewView(ctx, store,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFSIndex(fsidx),
		projection.WithRootView(cfg.RootView),
		projection.WithFallback(cfg.ByPathFallback),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: build view: %v\n", err)
		return 1
	}
	defer view.Close()

	mountSession := "mount-" + uuid.New().String()
	fmt.Fprintf(os.Stderr, "Mount session: %s\n", mountSession)

	scratchDir := cfg.ScratchDir
	if scratchDir == "" {
		scratchDir = filepath.Join(cfg.StorePath, ".scratch")
	}
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: mkdir scratch: %v\n", err)
		return 1
	}
	clearScratchDir(scratchDir)

	fsopsOpts := []projection.FSOpsOption{
		projection.WithStore(store),
		projection.WithScratchDir(scratchDir),
		projection.WithScratchQuota(cfg.ScratchQuota),
		projection.WithDefaultMode(cfg.DefaultMode),
		projection.WithDefaultUID(cfg.DefaultUID),
		projection.WithDefaultGID(cfg.DefaultGID),
		projection.WithEditingPolicy(cfg.EditingPolicy()),
		projection.WithMountSession(mountSession),
		projection.WithNamespace(cfg.Namespace),
	}
	if cfg.ReadOnly {
		fsopsOpts = append(fsopsOpts, projection.WithReadOnly())
	}
	fsops, err := projection.NewFSOps(view, fsopsOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: build fsops: %v\n", err)
		return 1
	}

	routingCfg := projection.RoutingConfig{
		ServicePrefix:   cfg.ServicePrefix,
		RootView:        cfg.RootView,
		ShowStats:       cfg.ShowStats,
		ShowByArtifact:  cfg.ShowByArtifact,
		ShowOrphaned:    cfg.ShowOrphaned,
		ShowByDate:      cfg.ShowByDate,
		ShowBySession:   cfg.ShowBySession,
		ShowByNamespace: cfg.ShowByNamespace,
		ShowRaw:         cfg.ShowRaw,
	}

	// statsProvider closes over the daemon's view of the
	// world: capacity is queried live (every read), extensions
	// are snapshotted on every read, the rest is the static
	// daemon config. This is what the user sees when they
	// open _scrinium/stats — see projection.RenderStats for
	// the format.
	startedAt := time.Now().UTC()
	statsProvider := func() []byte {
		// Capacity is best-effort: failure → render "n/a"
		// fields rather than fail the whole stats read.
		// Bound the call so a slow driver doesn't hang the
		// stats endpoint.
		capCtx, capCancel := context.WithTimeout(ctx, 2*time.Second)
		defer capCancel()
		var capPtr *domain.StorageInfo
		if cap, err := store.Capacity(capCtx); err == nil {
			capPtr = &cap
		}
		exts := make([]projection.ExtensionInfo, 0)
		for _, e := range idx.ListExtensions() {
			exts = append(exts, projection.ExtensionInfo{
				Name:          e.Name,
				SchemaVersion: e.SchemaVersion,
			})
		}
		return projection.RenderStats(view, projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: mountSession,
			StorePath:    cfg.StorePath,
			ReadOnly:     cfg.ReadOnly,
			Editing:      cfg.Editing,
			Namespace:    cfg.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}

	wfs := newWebdavFS(view, fsops, routingCfg, !cfg.AllowOSJunk, statsProvider)

	rejectJunk := !cfg.AllowOSJunk
	handler := &webdav.Handler{
		FileSystem: wfs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			// Suppress the noise that the OS-junk filter
			// generates by design: "missing" .DS_Store /
			// AppleDouble companion lookups, etc. Real errors
			// against real paths still land in the log.
			if rejectJunk && isOSJunk(cleanWebDAVPath(r.URL.Path)) {
				return
			}
			fmt.Fprintf(os.Stderr, "webdav: %s %s: %v\n", r.Method, r.URL.Path, err)
		},
	}

	// Build the top-level mux. WebDAV stays as the catch-all
	// at "/", so clients (Finder, rclone, Office) connect to
	// the daemon's root URL exactly as they would to a stock
	// WebDAV server. The browser, when configured, is mounted
	// under a separate prefix — a secondary surface for
	// human inspection that doesn't perturb WebDAV traffic.
	mux := http.NewServeMux()
	if cfg.BrowsePrefix != "" {
		webHandler := web.NewHandler(
			newWebBackingFS(wfs, store),
			cleanWebDAVPath,
			web.Config{
				StorePath:     cfg.StorePath,
				ServicePrefix: cfg.ServicePrefix,
				BrowsePrefix:  cfg.BrowsePrefix,
			},
		)
		// Register schema decoders the daemon understands.
		// Each domain has its own decoder; web stays
		// schema-agnostic and only consumes whatever the
		// host installs.
		webHandler.RegisterDecoder(fsmetaDecoder{})

		prefix := webHandler.Prefix()
		// Register both "/_browse" and "/_browse/" so requests
		// without the trailing slash are matched too. Go's
		// ServeMux would otherwise 404 the bare prefix.
		mux.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusMovedPermanently))
		mux.Handle(prefix+"/", webHandler)
		fmt.Fprintf(os.Stderr, "Browser: %s\n", prefix)
	}
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Signal handling for graceful shutdown.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "scrinium-webdav: shutting down...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stderr, "Serving WebDAV on %s\n", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	return 0
}

// clearScratchDir removes every entry inside dir without removing
// dir itself. Errors are swallowed: scratch eviction is a
// best-effort hygiene step.
func clearScratchDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// defaultHashRegistry returns a HashRegistry with the algorithms
// the daemon needs to read a Scrinium store written by reference
// hosts. Currently sha256 — the default content hasher in
// core.InitStore for unsealed stores. blake3 is mapped to sha256
// for parity with internal/testutil/storefx; production stores
// using a real blake3 implementation should use a custom daemon
// that registers the proper constructor.
func defaultHashRegistry() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

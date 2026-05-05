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

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver/localfs"
	"github.com/rkurbatov/scrinium/index/sqlite"
	"github.com/rkurbatov/scrinium/projection"
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

	wfs := newWebdavFS(view, fsops, routingCfg, !cfg.AllowOSJunk)

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

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
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

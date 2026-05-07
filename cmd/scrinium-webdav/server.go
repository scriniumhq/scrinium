package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"github.com/rkurbatov/scrinium/cmd/scrinium-webdav/web"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/daemon"
	"github.com/rkurbatov/scrinium/projection"
)

// runServe is the entry point for "scrinium-webdav serve". The
// heavy lifting (open store/index, build view, fsops, scratch
// dir, mount session) lives in internal/daemon.Open. This
// function is responsible only for the WebDAV-specific surface:
// the HTTP listener, the WebDAV adapter, the optional /_browse
// HTML view, and signal handling.
//
// Lifecycle:
//
//  1. Parse + validate config.
//  2. daemon.Open — bootstrap the shared resources.
//  3. Build the routing config from the daemon Config (still
//     a webdav-cmd concern: it owns _scrinium tree visibility).
//  4. Wrap d.FSOps as webdav.FileSystem; mount the WebDAV
//     handler at "/".
//  5. Optionally mount the embedded HTML browser under
//     cfg.BrowsePrefix.
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

	d, err := daemon.Open(ctx, cfg.Daemon)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	defer d.Close()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", d.MountSession)

	// Routing config is webdav-cmd's concern: it controls how
	// the _scrinium service tree appears in the browser/listing.
	// It mirrors fields from daemon.Config but with the
	// projection-typed RootView.
	routingCfg := projection.RoutingConfig{
		ServicePrefix:   d.Config.ServicePrefix,
		RootView:        d.Config.RootView,
		ShowStats:       d.Config.ShowStats,
		ShowByArtifact:  d.Config.ShowByArtifact,
		ShowOrphaned:    d.Config.ShowOrphaned,
		ShowByDate:      d.Config.ShowByDate,
		ShowBySession:   d.Config.ShowBySession,
		ShowByNamespace: d.Config.ShowByNamespace,
		ShowRaw:         d.Config.ShowRaw,
	}

	// statsProvider closes over the daemon's view of the world:
	// capacity is queried live (every read), extensions are
	// snapshotted on every read, the rest is the static config.
	startedAt := time.Now().UTC()
	statsProvider := func() []byte {
		// Capacity is best-effort: failure → render "n/a"
		// fields rather than fail the whole stats read.
		// Bound the call so a slow driver doesn't hang the
		// stats endpoint.
		capCtx, capCancel := context.WithTimeout(ctx, 2*time.Second)
		defer capCancel()
		var capPtr *domain.StorageInfo
		if cap, err := d.Store.Capacity(capCtx); err == nil {
			capPtr = &cap
		}
		exts := make([]projection.ExtensionInfo, 0)
		for _, e := range d.ListExtensions() {
			exts = append(exts, projection.ExtensionInfo{
				Name:          e.Name,
				SchemaVersion: e.SchemaVersion,
			})
		}
		return projection.RenderStats(d.View, projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: d.MountSession,
			StorePath:    d.Config.Store,
			ReadOnly:     d.Config.ReadOnly,
			Editing:      d.Config.Editing,
			Namespace:    d.Config.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}

	htmlStatsProvider := func() web.StatsData {
		capCtx, capCancel := context.WithTimeout(ctx, 2*time.Second)
		defer capCancel()
		var capPtr *domain.StorageInfo
		if cap, err := d.Store.Capacity(capCtx); err == nil {
			capPtr = &cap
		}
		exts := make([]web.StatsExtension, 0)
		for _, e := range d.ListExtensions() {
			exts = append(exts, web.StatsExtension{
				Name:          e.Name,
				SchemaVersion: e.SchemaVersion,
			})
		}
		return buildWebStatsData(d.View, capPtr, exts, startedAt, d.MountSession, cfg)
	}

	wfs := newWebdavFS(d.View, d.FSOps, routingCfg, !cfg.AllowOSJunk, statsProvider)

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

	mux := http.NewServeMux()
	if cfg.BrowsePrefix != "" {
		webHandler := web.NewHandler(
			newWebBackingFS(wfs, d.Store),
			cleanWebDAVPath,
			web.Config{
				StorePath:     d.Config.Store,
				ServicePrefix: d.Config.ServicePrefix,
				BrowsePrefix:  cfg.BrowsePrefix,
			},
		)
		webHandler.RegisterDecoder(fsmetaDecoder{})
		webHandler.SetStatsProvider(htmlStatsProvider)

		prefix := webHandler.Prefix()
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

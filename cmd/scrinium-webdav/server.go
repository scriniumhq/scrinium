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

	"github.com/rkurbatov/scrinium/cmd/internal/daemon"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/projection"
)

// runServe is the entry point for "scrinium-webdav serve". The
// heavy lifting (open store/index, build view, fsops, scratch
// dir, mount session) lives in cmd/internal/daemon.Open. This
// function owns only the WebDAV-specific surface: the HTTP
// listener and the WebDAV adapter on top of vfs.
//
// scrinium-webdav is a clean WebDAV protocol server. The HTML
// browser ("/_browse/") that earlier versions embedded has
// moved to scrinium-webview; run it alongside if you want a
// diagnostic UI on the same store. Service trees
// (_scrinium/by-date/, etc.) are still reachable when
// configured, but disabled by default — Finder/rclone don't
// want to see them in their listings.
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

	// Routing config snapshot. Service trees follow daemon
	// config — webdav defaults to all off so a vanilla
	// `scrinium-webdav serve` exposes only the user-visible
	// filesystem; admins enable specific trees with --show-X
	// flags when they want them.
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

	// Stats body for vfs-level _scrinium/stats reads. Only
	// rendered if ShowStats is on (otherwise routing rejects
	// the path).
	startedAt := time.Now().UTC()
	statsProvider := func() []byte {
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

	wfs := newWebdavFS(d.View, d.FSOps, routingCfg, !cfg.AllowOSJunk, statsProvider)

	rejectJunk := !cfg.AllowOSJunk
	handler := &webdav.Handler{
		FileSystem: wfs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			// Suppress the noise the OS-junk filter generates
			// by design: "missing" .DS_Store / AppleDouble
			// companion lookups, etc. Real errors against
			// real paths still land in the log.
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

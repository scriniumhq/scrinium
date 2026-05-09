package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rkurbatov/scrinium"
	"github.com/rkurbatov/scrinium/cmd/scrinium-webview/web"
	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/projection/vfs"
)

// runServe is the entry point for "scrinium-webview serve".
// It wires daemon → vfs → BackingFS adapter → HTML handler
// in the order a downstream tool would, then starts an HTTP
// listener.
//
// Read-only: regardless of what the user wrote in
// cfg.Daemon.ReadOnly or --editing, runServe forces the
// daemon into ReadOnly mode. webview is a diagnostic
// surface; mutations belong on webdav/fuse.
func runServe(args []string) int {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webview serve: %v\n", err)
		return 2
	}

	// Force read-only — webview never writes.
	cfg.Daemon.ReadOnly = true
	cfg.Daemon.Editing = "off"

	// Force the text stats virtual file off — webview's
	// stats live as an HTML page via web handler, not as a
	// _scrinium/stats text file. The text variant is useful
	// in webdav/fuse where surfaces have no other way to
	// surface diagnostics; here it's redundant noise.
	cfg.Daemon.ShowStats = false

	// Force ServicePrefix off. webview shows every tree on
	// equal footing at the root — /by-path/, /by-date/,
	// /by-session/ etc. The "_scrinium/" prefix that other
	// surfaces use to keep service trees out of user
	// content space is unnecessary here: the entire surface
	// is service.
	cfg.Daemon.ServicePrefix = ""

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := scrinium.Open(ctx, cfg.Daemon)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webview: %v\n", err)
		return 1
	}
	defer d.Close()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", d.MountSession)

	// Routing config — start from defaults, then override the
	// two webview-specific fields. Webview lives at the URL
	// root rather than under a service prefix; service trees
	// are reachable as /by-path/, /by-date/ etc.
	routingCfg := d.RoutingConfig()
	routingCfg.ServicePrefix = ""
	routingCfg.UnprefixedServiceTrees = true

	startedAt := time.Now().UTC()

	// Plain-text stats body for vfs-level _scrinium/stats reads.
	// d.Config.ReadOnly is true (forced above) so the rendered
	// "ReadOnly" line matches webview's actual posture.
	statsProvider := d.StatsProvider(ctx, startedAt, 2*time.Second)

	// HTML stats provider. Same data, different rendering — the
	// browser uses its own type (web.StatsData) which the
	// scrinium top-level package does not know about, so the
	// inline definition stays.
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

	// Build the VFS — the substrate the web handler reads
	// through. No NameFilter (webview shows everything),
	// stats provider attached.
	v := vfs.New(d.View, d.FSOps, routingCfg, vfs.WithStatsProvider(statsProvider))

	// Web handler over a backing fs that talks to vfs.
	backing := newWebBackingFS(v, d.Store)
	webHandler := web.NewHandler(backing, vfs.CleanPath, web.Config{
		StorePath:     d.Config.Store,
		ServicePrefix: d.Config.ServicePrefix,
		BrowsePrefix:  cfg.BrowsePrefix,
	})
	webHandler.RegisterDecoder(fsmetaDecoder{})
	webHandler.SetStatsProvider(htmlStatsProvider)

	// HTTP routing.
	//
	//   - bare BrowsePrefix ("/" or "/_browse") redirects to
	//     the configured DefaultTree;
	//   - everything under BrowsePrefix goes to the web
	//     handler, which serves listings, artifact pages,
	//     stats and search.
	//
	// The "by-path" tree lives at the URL root because of
	// UnprefixedServiceTrees + RootView=ByPath; "by-date",
	// "by-session" etc. live at /<tree>/. The redirect
	// translates DefaultTree into the right URL.
	defaultTree := cfg.DefaultTree
	if defaultTree == "" {
		defaultTree = "by-path"
	}
	rootRedirect := redirectURL(cfg.BrowsePrefix, defaultTree)

	mux := http.NewServeMux()
	if cfg.BrowsePrefix == "/" {
		// "/" redirects, anything else under root → web
		// handler. The mux dispatches "/" exactly to the
		// redirect; everything else falls through to the
		// catch-all handler we register on it as a
		// HandlerFunc.
		mux.Handle("/", redirectOrWebHandler(rootRedirect, webHandler))
	} else {
		prefix := webHandler.Prefix()
		mux.Handle(prefix, http.RedirectHandler(rootRedirect, http.StatusFound))
		mux.Handle(prefix+"/", webHandler)
		mux.Handle("/", http.RedirectHandler(rootRedirect, http.StatusFound))
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "scrinium-webview: shutting down...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stderr, "Serving HTML view on %s\n", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "scrinium-webview: %v\n", err)
		return 1
	}
	return 0
}

// redirectURL builds the absolute URL the bare BrowsePrefix
// redirects to. Used as the destination both at "/" and at
// "/<browsePrefix>".
//
// All trees — including by-path — live at /<tree>/ in the
// webview's URL space. We don't redirect to "/" itself
// because that would loop back through the redirect handler.
func redirectURL(browsePrefix, tree string) string {
	prefix := strings.TrimSuffix(browsePrefix, "/")
	if prefix == "" {
		return "/" + tree + "/"
	}
	return prefix + "/" + tree + "/"
}

// redirectOrWebHandler is the dispatcher used when
// BrowsePrefix is "/" — i.e. the web handler claims the
// entire URL space. We can't register both an exact "/" and
// a catch-all "/" with ServeMux, so we route inside one
// HandlerFunc:
//
//   - exact "/" → 302 to the configured DefaultTree;
//   - everything else → web handler.
//
// The web handler handles everything from /by-path onward
// (and /by-date, etc.), so the redirect is only triggered
// on the first hit at the bare root.
func redirectOrWebHandler(redirect string, web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, redirect, http.StatusFound)
			return
		}
		web.ServeHTTP(w, r)
	})
}

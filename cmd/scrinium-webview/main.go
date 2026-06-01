package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"strings"
	"syscall"
	"time"

	"scrinium.dev"

	"scrinium.dev/cmd/internal/daemon"
	"scrinium.dev/cmd/scrinium-webview/web"
	"scrinium.dev/domain"
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
	"scrinium.dev/projection/vfs"
)

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "scrinium-webview: unknown command %q\n\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to a Scrinium YAML configuration file (required).")
	listen := fs.String("listen", ":8081", "HTTP listen address.")
	browsePrefix := fs.String("browse-prefix", "/", "URL prefix to serve the browser under (\"/\" for root).")
	defaultTree := fs.String("default-tree", "by-path", "Tree the bare browse prefix redirects to.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webview serve: --config is required")
		return 2
	}
	if *browsePrefix == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webview serve: --browse-prefix is required (use \"/\" for root)")
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webview serve: read config: %v\n", err)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	asm, err := scrinium.LoadOrInitYAML(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webview: %v\n", err)
		return 1
	}
	defer asm.Close()

	if asm.Projection == nil {
		fmt.Fprintln(os.Stderr, "scrinium-webview: config has no projection section; nothing to serve")
		return 1
	}

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", asm.MountSession)

	startedAt := time.Now().UTC()
	meta := asm.Info

	// webview is read-only and opinionated about layout: it lives at the
	// URL root with every tree shown unprefixed (/by-path/, /by-date/,
	// …) and the text stats file off (it renders stats as HTML instead).
	// These are properties of THIS surface, not of the stored data, so
	// they are set here rather than in the config.
	routingCfg := vfs.Config{
		ServicePrefix:          "",
		ShowStats:              false,
		ShowByArtifact:         true,
		ShowOrphaned:           true,
		ShowByDate:             true,
		ShowBySession:          true,
		ShowByNamespace:        true,
		ShowRaw:                true,
		UnprefixedServiceTrees: true,
	}

	textStats := daemon.StatsProvider(asm, startedAt, 2*time.Second)
	htmlStats := func() web.StatsData {
		capCtx, capCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer capCancel()
		var capPtr *domain.StorageInfo
		if c, err := asm.Store.Capacity(capCtx); err == nil {
			capPtr = &c
		}
		exts := make([]web.StatsExtension, 0)
		for _, e := range asm.Extensions() {
			exts = append(exts, web.StatsExtension{Name: e.Name, SchemaVersion: e.SchemaVersion})
		}
		// webview is always read-only; reflect that on the page.
		return buildWebStatsData(asm.Projection.Queries(), capPtr, exts, startedAt, asm.MountSession,
			meta.StoreURI, true, "off", meta.Namespace)
	}

	v := vfs.New(asm.Projection.View, asm.Projection.FSOps, routingCfg, vfs.WithStatsProvider(textStats))
	backing := newWebBackingFS(v, asm.Projection.Queries(), asm.Store)
	webHandler := web.NewHandler(backing, vfs.CleanPath, web.Config{
		StorePath:     meta.StoreURI,
		ServicePrefix: "",
		BrowsePrefix:  *browsePrefix,
	})
	webHandler.RegisterDecoder(fsmetaDecoder{})
	webHandler.SetStatsProvider(htmlStats)

	tree := *defaultTree
	if tree == "" {
		tree = "by-path"
	}
	rootRedirect := redirectURL(*browsePrefix, tree)

	mux := http.NewServeMux()
	if *browsePrefix == "/" {
		mux.Handle("/", redirectOrWebHandler(rootRedirect, webHandler))
	} else {
		prefix := webHandler.Prefix()
		mux.Handle(prefix, http.RedirectHandler(rootRedirect, http.StatusFound))
		mux.Handle(prefix+"/", webHandler)
		mux.Handle("/", http.RedirectHandler(rootRedirect, http.StatusFound))
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutCtx)
	}()

	fmt.Fprintf(os.Stderr, "Serving HTML view on %s\n", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "scrinium-webview: %v\n", err)
		return 1
	}
	return 0
}

const usageText = `scrinium-webview — read-only HTML browser for a Scrinium store.

Usage:
  scrinium-webview serve --config <file> [--listen :8081] [--browse-prefix /] [--default-tree by-path]

The config describes the store and projection.
Serving options are flags.

Specification: docs/4 §16 WebView.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

// redirectURL builds the absolute URL the bare browse prefix redirects
// to. All trees live at /<tree>/ in webview's URL space.
func redirectURL(browsePrefix, tree string) string {
	prefix := strings.TrimSuffix(browsePrefix, "/")
	if prefix == "" {
		return "/" + tree + "/"
	}
	return prefix + "/" + tree + "/"
}

// redirectOrWebHandler dispatches when the browse prefix is "/": an
// exact "/" redirects to the default tree, everything else hits the web
// handler (ServeMux can't host both an exact and a catch-all "/").
func redirectOrWebHandler(redirect string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, redirect, http.StatusFound)
			return
		}
		h.ServeHTTP(w, r)
	})
}

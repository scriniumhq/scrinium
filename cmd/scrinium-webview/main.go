package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"scrinium.dev/cmd/internal/daemon"
	"scrinium.dev/cmd/scrinium-webview/internal/web"
	"scrinium.dev/domain"
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
	"scrinium.dev/projection/vfs"
)

const name = "scrinium-webview"

func main() {
	os.Exit(daemon.Dispatch(name, usageText, map[string]daemon.Command{
		"serve": runServe,
	}))
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
	if *browsePrefix == "" {
		fmt.Fprintf(os.Stderr, "%s serve: --browse-prefix is required (use \"/\" for root)\n", name)
		return 2
	}

	asm, ctx, stop, code := daemon.Load(name, *configPath, true)
	if code != 0 {
		return code
	}
	defer stop()

	startedAt := time.Now().UTC()
	meta := asm.Info

	// webview is read-only and opinionated about layout: it lives at the
	// URL root with every tree shown unprefixed (/by-date/,
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
		ShowProvidedViews:      true,
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
		for _, d := range asm.Extensions() {
			exts = append(exts, web.StatsExtension{Name: d.Name})
		}
		sysArts := gatherSystemArtifacts(capCtx, asm.Store.System())
		// webview is always read-only; reflect that on the page.
		return buildWebStatsData(asm.Projection.Queries(), capPtr, exts, sysArts, startedAt, asm.MountSession,
			meta.StoreURI, true, "off")
	}

	v := vfs.New(asm.Projection, routingCfg, vfs.WithStatsProvider(textStats))
	backing := newWebBackingFS(v, asm.Projection.Queries(), asm.Store.System())

	// Nav tabs reflect what is actually mounted: the intrinsic browsable trees
	// the routing config exposes, plus whatever roots the connected extensions
	// provide (by-path from fspath, by-namespace from namespace, …). A tab for
	// a root no extension backs would 404, so the list is derived, not fixed.
	browseRoots := make([]string, 0, 5)
	if routingCfg.ShowByDate {
		browseRoots = append(browseRoots, "by-date")
	}
	if routingCfg.ShowBySession {
		browseRoots = append(browseRoots, "by-session")
	}
	if routingCfg.ShowByArtifact {
		browseRoots = append(browseRoots, "by-artifact")
	}
	if routingCfg.ShowProvidedViews {
		for _, r := range asm.Projection.View.ProvidedRoots() {
			browseRoots = append(browseRoots, string(r))
		}
	}
	sort.Strings(browseRoots)

	webHandler := web.NewHandler(backing, vfs.CleanPath, web.Config{
		StorePath:     meta.StoreURI,
		ServicePrefix: "",
		BrowsePrefix:  *browsePrefix,
		Roots:         browseRoots,
	})
	webHandler.RegisterDecoder(vfsmetaDecoder{})
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
	fmt.Fprintf(os.Stderr, "Serving HTML view on %s\n", *listen)
	return daemon.ServeHTTP(ctx, name, srv)
}

const usageText = `scrinium-webview — read-only HTML browser for a Scrinium store.

Usage:
  scrinium-webview serve --config <file> [--listen :8081] [--browse-prefix /] [--default-tree by-path]

The config describes the store and projection.
Serving options are flags.

Specification: docs/4 §16 WebView.
`

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

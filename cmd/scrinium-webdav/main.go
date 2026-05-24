// Command scrinium-webdav exposes a Scrinium store over WebDAV —
// cross-platform access (Finder, Windows Explorer, rclone) without a
// kernel extension or root.
//
// The store/projection is described by a composer config document; how
// it is served (listen address, OS-junk filtering) is given by flags.
// This split is deliberate: the config says WHAT is stored and how it
// is projected, the daemon decides HOW to expose it.
//
//	scrinium-webdav serve --config store.yaml --listen :8080
//
// This file is a reference implementation. It is intentionally small
// and self-contained: copy the package and adapt it to wrap a Scrinium
// store in your own service. The reusable parts live in composer
// (assembly) and engine/projection; everything here is glue you are
// meant to own.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"scrinium.dev/composer"
	"scrinium.dev/engine/assembly"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/projection"
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
		fmt.Fprintf(os.Stderr, "scrinium-webdav: unknown command %q\n\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to a composer YAML config file (required).")
	listen := fs.String("listen", ":8080", "HTTP listen address.")
	allowOSJunk := fs.Bool("allow-os-junk", false, "Permit .DS_Store / Thumbs.db writes instead of filtering them.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webdav serve: --config is required")
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav serve: read config: %v\n", err)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	asm, err := composer.LoadOrInitYAML(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	defer asm.Close()

	if asm.FSOps() == nil {
		fmt.Fprintln(os.Stderr, "scrinium-webdav: config has no projection section; nothing to serve")
		return 1
	}

	startedAt := time.Now().UTC()
	// WebDAV exposes only the user filesystem: every diagnostic tree is
	// off so Finder/rclone see a clean root. Service prefix is kept for
	// the stats pseudo-file path only.
	routingCfg := projection.RoutingConfig{
		ServicePrefix: "_scrinium",
		RootView:      projection.RootByPath,
	}
	stats := statsProvider(asm, startedAt, 2*time.Second)
	wfs := newWebdavFS(asm.View(), asm.FSOps(), routingCfg, !*allowOSJunk, stats)

	handler := &webdav.Handler{
		FileSystem: wfs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			if !*allowOSJunk && isOSJunk(cleanWebDAVPath(r.URL.Path)) {
				return
			}
			fmt.Fprintf(os.Stderr, "webdav: %s %s: %v\n", r.Method, r.URL.Path, err)
		},
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutCtx)
	}()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", asm.MountSession())
	fmt.Fprintf(os.Stderr, "Serving WebDAV on %s\n", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	return 0
}

// statsProvider renders the assembly's stats snapshot for the
// _scrinium/stats pseudo-file. capacityTimeout caps Store.Capacity so a
// slow driver never hangs a stats read; on error capacity is omitted.
func statsProvider(asm assembly.Assembly, startedAt time.Time, capacityTimeout time.Duration) func() []byte {
	return func() []byte {
		capCtx, cancel := context.WithTimeout(context.Background(), capacityTimeout)
		defer cancel()

		var capPtr *domain.StorageInfo
		if info, err := asm.Store().Capacity(capCtx); err == nil {
			capPtr = &info
		}

		exts := make([]projection.ExtensionInfo, 0)
		if lister, ok := asm.Index().(index.ExtensionLister); ok {
			for _, e := range lister.ListExtensions() {
				exts = append(exts, projection.ExtensionInfo{Name: e.Name, SchemaVersion: e.SchemaVersion})
			}
		}

		meta := asm.Info()
		return projection.RenderStats(asm.View(), projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: asm.MountSession(),
			StorePath:    meta.StoreURI,
			ReadOnly:     meta.ReadOnly,
			Editing:      meta.Editing,
			Namespace:    meta.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}
}

const usageText = `scrinium-webdav — expose a Scrinium store over WebDAV.

Usage:
  scrinium-webdav serve --config <file> [--listen :8080] [--allow-os-junk]

The config describes the store and projection (a composer document).
Serving options are flags.

Specification: docs/4 §15 WebDAV Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

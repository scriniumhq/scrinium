package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"scrinium.dev/projection/node"
	"scrinium.dev/projection/routing"

	"syscall"
	"time"

	"golang.org/x/net/webdav"
	"scrinium.dev"

	// Built-in backends register by blank import (ADR-63).
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"

	"scrinium.dev/cmd/internal/daemon"
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
	configPath := fs.String("config", "", "Path to a Scrinium YAML configuration file (required).")
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

	asm, err := scrinium.LoadOrInitYAML(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	defer asm.Close()

	if asm.Projection == nil {
		fmt.Fprintln(os.Stderr, "scrinium-webdav: config has no projection section; nothing to serve")
		return 1
	}

	startedAt := time.Now().UTC()
	// WebDAV exposes only the user filesystem: every diagnostic tree is
	// off so Finder/rclone see a clean root. Service prefix is kept for
	// the stats pseudo-file path only.
	routingCfg := routing.Config{
		ServicePrefix: "_scrinium",
		RootView:      node.RootByPath,
	}
	stats := daemon.StatsProvider(asm, startedAt, 2*time.Second)
	wfs := newWebdavFS(asm.Projection.View, asm.Projection.FSOps, routingCfg, !*allowOSJunk, stats)

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

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", asm.MountSession)
	fmt.Fprintf(os.Stderr, "Serving WebDAV on %s\n", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	return 0
}

const usageText = `scrinium-webdav — expose a Scrinium store over WebDAV.

Usage:
  scrinium-webdav serve --config <file> [--listen :8080] [--allow-os-junk]

The config describes the store and projection.
Serving options are flags.

Specification: docs/4 §15 WebDAV Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

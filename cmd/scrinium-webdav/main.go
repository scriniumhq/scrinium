package main

import (
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/webdav"

	// Built-in backends register by blank import (ADR-63).
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"

	"scrinium.dev/cmd/internal/daemon"
	"scrinium.dev/projection/vfs"
)

const name = "scrinium-webdav"

func main() {
	os.Exit(daemon.Dispatch(name, usageText, map[string]daemon.Command{
		"serve": runServe,
	}))
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

	asm, ctx, stop, code := daemon.Load(name, *configPath, true)
	if code != 0 {
		return code
	}
	defer stop()

	startedAt := time.Now().UTC()
	// WebDAV exposes only the user filesystem: every diagnostic tree is
	// off so Finder/rclone see a clean root. Service prefix is kept for
	// the stats pseudo-file path only.
	routingCfg := vfs.Config{
		ServicePrefix: "_scrinium",
	}
	stats := daemon.StatsProvider(asm, startedAt, 2*time.Second)
	wfs := newWebdavFS(asm.Projection, routingCfg, !*allowOSJunk, stats)

	handler := &webdav.Handler{
		FileSystem: wfs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			if errors.Is(err, iofs.ErrNotExist) {
				return // a 404 is normal WebDAV traffic, not a fault
			}
			if !*allowOSJunk && isOSJunk(vfs.CleanPath(r.URL.Path)) {
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
	fmt.Fprintf(os.Stderr, "Serving WebDAV on %s\n", *listen)
	return daemon.ServeHTTP(ctx, name, srv)
}

const usageText = `scrinium-webdav — expose a Scrinium store over WebDAV.

Usage:
  scrinium-webdav serve --config <file> [--listen :8080] [--allow-os-junk]

The config describes the store and projection.
Serving options are flags.

Specification: docs/4 §15 WebDAV Mount.
`

package main

import (
	"errors"
	"flag"
	"io/fs"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/webdav"

	// Built-in backends register by blank import (ADR-63).
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"

	"scrinium.dev/cmd/internal/clog"
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
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "Path to a Scrinium YAML configuration file (required).")
	listen := flags.String("listen", ":8080", "HTTP listen address.")
	allowOSJunk := flags.Bool("allow-os-junk", false, "Permit .DS_Store / Thumbs.db writes instead of filtering them.")
	debug := flags.Bool("debug", clog.EnvDebug(), "Log every request (method, status, duration); off shows only errors.")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	asm, ctx, stop, code := daemon.Load(name, *configPath, true)
	if code != 0 {
		return code
	}
	defer stop()

	log := clog.New(*debug)
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
			// A 404 is normal traffic; only real faults reach Error. The
			// per-request trace (incl. 404s) is the middleware's job under
			// --debug.
			if err == nil || errors.Is(err, fs.ErrNotExist) {
				return
			}
			if !*allowOSJunk && isOSJunk(vfs.CleanPath(r.URL.Path)) {
				return
			}
			log.Error("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
		},
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           clog.Middleware(log, "webdav")(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("serving webdav", "addr", *listen)
	return daemon.ServeHTTP(ctx, name, srv)
}

const usageText = `scrinium-webdav — expose a Scrinium store over WebDAV.

Usage:
  scrinium-webdav serve --config <file> [--listen :8080] [--allow-os-junk]

The config describes the store and projection.
Serving options are flags.

Specification: docs/4 §15 WebDAV Mount.
`

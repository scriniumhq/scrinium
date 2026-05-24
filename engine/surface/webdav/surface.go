package webdav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/webdav"

	"scrinium.dev/composer"
	"scrinium.dev/engine/runtime"
	"scrinium.dev/engine/surface/internal/surfacekit"
)

// init registers the "webdav" surface kind with composer so a
// `surfaces: [{kind: webdav, config: {...}}]` block assembles this
// server. Hosts pull it in with a side-effect import:
//
//	import _ "scrinium.dev/engine/surface/webdav"
func init() {
	composer.RegisterSurface("webdav", New)
}

// config is the webdav surface's own config block. Service-tree
// visibility (the embedded surfacekit.Routing) defaults every tree OFF
// for WebDAV: a vanilla mount exposes only the user filesystem, since
// Finder / rclone do not want the diagnostic-tree noise. That is the
// per-surface override the hybrid projection model allows.
type config struct {
	Listen      string `yaml:"listen"`
	AllowOSJunk bool   `yaml:"allowOsJunk"`

	surfacekit.Routing `yaml:",inline"`
}

// New builds the WebDAV surface from the assembled runtime and its
// config block. It is the SurfaceFactory registered for "webdav".
func New(rt runtime.Runtime, raw map[string]any) (runtime.Surface, error) {
	// Routing defaults: trees off, by-path root, standard prefix.
	c := config{
		Listen:  ":8080",
		Routing: surfacekit.Routing{ServicePrefix: "_scrinium", RootView: "by-path"},
	}
	if err := surfacekit.DecodeConfig(raw, &c); err != nil {
		return nil, fmt.Errorf("webdav: config: %w", err)
	}
	if c.Listen == "" {
		return nil, fmt.Errorf("webdav: listen is required")
	}
	if rt.FSOps() == nil {
		return nil, fmt.Errorf("webdav: runtime has no FSOps; add a projection section to the config")
	}

	rejectJunk := !c.AllowOSJunk
	stats := surfacekit.StatsProvider(rt, time.Now().UTC(), 2*time.Second)
	wfs := newWebdavFS(rt.View(), rt.FSOps(), c.Routing.Config(), rejectJunk, stats)

	handler := &webdav.Handler{
		FileSystem: wfs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			// Suppress the noise the OS-junk filter generates by
			// design (missing .DS_Store / AppleDouble lookups); real
			// errors against real paths still log.
			if rejectJunk && isOSJunk(cleanWebDAVPath(r.URL.Path)) {
				return
			}
			fmt.Fprintf(os.Stderr, "webdav: %s %s: %v\n", r.Method, r.URL.Path, err)
		},
	}

	return &surface{
		listen: c.Listen,
		srv: &http.Server{
			Addr:              c.Listen,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}, nil
}

// surface is the running WebDAV server as a runtime.Surface.
type surface struct {
	listen string
	srv    *http.Server
}

var _ runtime.Surface = (*surface)(nil)

func (s *surface) Name() string { return "webdav" }

// Serve runs the HTTP server until ctx is cancelled (graceful
// shutdown) or it fails. A clean ctx-driven shutdown returns nil.
func (s *surface) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()
	fmt.Fprintf(os.Stderr, "Serving WebDAV on %s\n", s.listen)
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close shuts the server down (idempotent at the http.Server level).
func (s *surface) Close() error {
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.srv.Shutdown(shutCtx)
}

//go:build linux || darwin

package fuse

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"scrinium.dev/composer"
	"scrinium.dev/engine/runtime"
	"scrinium.dev/engine/surface/internal/surfacekit"
)

// init registers the "fuse" surface kind with composer. Pull it in
// with a side-effect import (Linux/macOS only):
//
//	import _ "scrinium.dev/engine/surface/fuse"
func init() {
	composer.RegisterSurface("fuse", New)
}

// config is the fuse surface's own config block. Unlike WebDAV, FUSE
// defaults every service tree ON — a desktop mount is the place users
// expect to browse _scrinium/by-date/ etc.
type config struct {
	MountPoint string `yaml:"mountPoint"`
	AllowOther bool   `yaml:"allowOther"`

	surfacekit.Routing `yaml:",inline"`
}

// New builds the FUSE surface. It is the SurfaceFactory registered for
// "fuse". The actual mount happens in Serve, not here, so a failed
// runtime assembly never leaves a dangling kernel mount.
func New(rt runtime.Runtime, raw map[string]any) (runtime.Surface, error) {
	c := config{
		Routing: surfacekit.Routing{
			ServicePrefix:   "_scrinium",
			RootView:        "by-path",
			ShowStats:       true,
			ShowByArtifact:  true,
			ShowOrphaned:    true,
			ShowByDate:      true,
			ShowBySession:   true,
			ShowByNamespace: true,
			ShowRaw:         true,
		},
	}
	if err := surfacekit.DecodeConfig(raw, &c); err != nil {
		return nil, fmt.Errorf("fuse: config: %w", err)
	}
	if c.MountPoint == "" {
		return nil, fmt.Errorf("fuse: mountPoint is required")
	}
	if rt.FSOps() == nil {
		return nil, fmt.Errorf("fuse: runtime has no FSOps; add a projection section to the config")
	}

	startedAt := time.Now().UTC()
	root := &rootNode{
		view:          rt.View(),
		fsops:         rt.FSOps(),
		store:         rt.Store(),
		routingCfg:    c.Routing.Config(),
		startedAt:     startedAt,
		statsProvider: surfacekit.StatsProvider(rt, startedAt, 2*time.Second),
	}

	return &surface{
		mountPoint: c.MountPoint,
		root:       root,
		mountOpts: &fs.Options{
			MountOptions: fuse.MountOptions{
				AllowOther: c.AllowOther,
				Name:       "scrinium",
				FsName:     rt.Info().StoreURI,
			},
		},
	}, nil
}

// surface is the FUSE mount as a runtime.Surface.
type surface struct {
	mountPoint string
	root       *rootNode
	mountOpts  *fs.Options

	mu     sync.Mutex
	server *fuse.Server
}

var _ runtime.Surface = (*surface)(nil)

func (s *surface) Name() string { return "fuse" }

// Serve mounts the filesystem and blocks until ctx is cancelled (which
// triggers an Unmount) or the kernel detaches it. A clean ctx-driven
// shutdown returns nil.
func (s *surface) Serve(ctx context.Context) error {
	server, err := fs.Mount(s.mountPoint, s.root, s.mountOpts)
	if err != nil {
		return fmt.Errorf("mount %s: %w", s.mountPoint, err)
	}
	s.mu.Lock()
	s.server = server
	s.mu.Unlock()
	fmt.Fprintf(os.Stderr, "Mounted at %s\n", s.mountPoint)

	go func() {
		<-ctx.Done()
		_ = server.Unmount()
	}()
	server.Wait()
	return nil
}

// Close unmounts the filesystem if it is still mounted. Safe to call
// after a ctx-driven shutdown (the second Unmount is a no-op error we
// ignore).
func (s *surface) Close() error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	_ = server.Unmount()
	return nil
}

// inodeFor maps a (tree, subPath) pair to a deterministic 64-bit inode
// number. fnv-64 suffices for a single mount session; go-fuse dedupes
// on (parent, name) at lookup, tolerating the rare collision. inode 1
// is the mount root (FUSE convention); the reserved low range (1..15)
// is avoided.
func inodeFor(tree string, subPath string) uint64 {
	if tree == "" && subPath == "" {
		return 1
	}
	h := fnv.New64a()
	h.Write([]byte(tree))
	h.Write([]byte{0})
	h.Write([]byte(subPath))
	v := h.Sum64()
	if v < 16 {
		v += 16
	}
	return v
}

// cleanName strips surrounding slashes (defensive; go-fuse passes bare
// names).
func cleanName(s string) string {
	return strings.Trim(s, "/")
}

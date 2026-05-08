//go:build fuse && (linux || darwin)

package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/daemon"
	"github.com/rkurbatov/scrinium/projection"
)

// runMount with the FUSE backend wired in. The heavy lifting
// (open store, build view, fsops, scratch dir, mount session)
// lives in cmd/internal/daemon.Open. This function owns only the
// FUSE-specific surface: the root inode tree, the mount, and
// signal handling.
//
// Lifecycle:
//
//  1. Parse + validate config.
//  2. daemon.Open — bootstrap the shared resources.
//  3. Build the routing config from daemon Config.
//  4. Construct the root inode tree and mount.
//  5. Block on the FUSE server, propagating SIGINT/SIGTERM
//     as a graceful Unmount.
func runMount(args []string) int {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse mount: %v\n", err)
		return 2
	}

	ctx := context.Background()

	d, err := daemon.Open(ctx, cfg.Daemon)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: %v\n", err)
		return 1
	}
	defer d.Close()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", d.MountSession)

	// Routing config snapshot for the dispatcher.
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

	startedAt := time.Now().UTC()
	statsProvider := func() []byte {
		// Capacity is best-effort: failure → "n/a" fields
		// rather than fail the whole stats read. Bound the
		// call so a slow driver doesn't hang the user's
		// `cat _scrinium/stats`.
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
			Namespace:    d.Config.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}

	root := &rootNode{
		view:          d.View,
		fsops:         d.FSOps,
		store:         d.Store,
		routingCfg:    routingCfg,
		startedAt:     startedAt,
		statsProvider: statsProvider,
	}

	mountOpts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: cfg.AllowOther,
			Name:       "scrinium",
			FsName:     d.Config.Store,
		},
	}

	server, err := fs.Mount(cfg.MountPoint, root, mountOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: mount: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Mounted at %s\n", cfg.MountPoint)

	// SIGINT/SIGTERM trigger a graceful unmount. fs.Server.Wait
	// blocks until either the server exits on its own or we
	// tell it to via Unmount.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "scrinium-fuse: shutting down...")
		_ = server.Unmount()
	}()
	server.Wait()
	return 0
}

// inodeFor maps a (tree, subPath) pair to a deterministic
// 64-bit inode number. fnv-64 is more than enough for the
// cardinalities of a single mount session; collisions
// translate to "two virtual paths share an inode", which
// go-fuse tolerates in practice because it dedupes on
// (parent, name) at lookup time.
//
// inode 1 is reserved for the mount root (FUSE convention).
// Service-root and stats nodes have fixed values to keep
// their inodes stable across remounts — useful for tools
// that cache `stat` output.
func inodeFor(tree string, subPath string) uint64 {
	if tree == "" && subPath == "" {
		return 1
	}
	h := fnv.New64a()
	h.Write([]byte(tree))
	h.Write([]byte{0})
	h.Write([]byte(subPath))
	v := h.Sum64()
	// Avoid the reserved low-numbered range (1..15).
	if v < 16 {
		v += 16
	}
	return v
}

// joinTreePath glues a tree subpath together. parentSub is
// the parent path within the tree ("" = tree root), child is
// the last segment to append.
func joinTreePath(parentSub, child string) string {
	if parentSub == "" {
		return child
	}
	if child == "" {
		return parentSub
	}
	return parentSub + "/" + child
}

// cleanName strips an extra trailing slash if any (defensive
// — go-fuse passes names without slashes, but we want
// robustness against future callers).
func cleanName(s string) string {
	return strings.Trim(s, "/")
}

//go:build fuse && (linux || darwin)

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"hash/fnv"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/google/uuid"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver/localfs"
	"github.com/rkurbatov/scrinium/index/sqlite"
	"github.com/rkurbatov/scrinium/projection"
	"github.com/rkurbatov/scrinium/projection/fsindex"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// runMount with the FUSE backend wired in.  Builds the View, the
// FSOps, generates a mount session and hands the assembled root
// inode to fs.Mount.
//
// Lifecycle:
//  1. Parse + validate config.
//  2. Open the Store.
//  3. Build the View (synchronous backfill).
//  4. Build the FSOps with the configured policy.
//  5. Construct the root inode tree and mount.
//  6. Block on the FUSE server, propagating SIGINT/SIGTERM as
//     a graceful Unmount.
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

	// Build the dependencies for core.OpenStore. cmd/scrinium-fuse
	// is a reference daemon: it picks localfs as the driver and
	// sqlite as the index, both rooted under cfg.StorePath. Hosts
	// that need other backends (S3 driver, postgres index) write
	// their own daemon — the FSOps/View layers below are
	// driver-agnostic.
	drv, err := localfs.New(cfg.StorePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: localfs driver: %v\n", err)
		return 1
	}
	idx, err := sqlite.NewStore(ctx, filepath.Join(cfg.StorePath, "index.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: sqlite index: %v\n", err)
		return 1
	}

	// Register the filesystem-projection index extension. fsindex
	// persists each artifact's fsmeta payload alongside the main
	// index in the same transaction, so View backfill can fetch
	// metadata in bulk instead of round-tripping Source.Get for
	// every manifest. Registration must happen before OpenStore
	// so the very first IndexManifest dispatches into fsindex.
	fsidx := fsindex.New()
	if err := idx.Extensions().Register(ctx, fsidx); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: register fsindex: %v\n", err)
		return 1
	}

	store, err := core.OpenStore(ctx, drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(defaultHashRegistry()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: open store: %v\n", err)
		return 1
	}

	// TODO encrypted-store unlock via cfg.PassphraseFile lands
	// once we wire core.Unlock; for 5b we assume an unlocked store.

	view, err := projection.NewView(ctx, store,
		projection.WithPathResolver(fsmeta.Resolver),
		projection.WithFSIndex(fsidx),
		projection.WithRootView(cfg.RootView),
		projection.WithFallback(cfg.ByPathFallback),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: build view: %v\n", err)
		return 1
	}
	defer view.Close()

	mountSession := "mount-" + uuid.New().String()
	fmt.Fprintf(os.Stderr, "Mount session: %s\n", mountSession)

	scratchDir := cfg.ScratchDir
	if scratchDir == "" {
		scratchDir = filepath.Join(cfg.StorePath, ".scratch")
	}
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: mkdir scratch: %v\n", err)
		return 1
	}
	// Recreate-at-mount: clear any leftovers from a previous
	// session. Failures here are non-fatal — the underlying FS
	// may complain about specific files, scratch will still
	// work.
	clearScratch(scratchDir)

	fsopsOpts := []projection.FSOpsOption{
		projection.WithStore(store),
		projection.WithScratchDir(scratchDir),
		projection.WithScratchQuota(cfg.ScratchQuota),
		projection.WithDefaultMode(cfg.DefaultMode),
		projection.WithDefaultUID(cfg.DefaultUID),
		projection.WithDefaultGID(cfg.DefaultGID),
		projection.WithEditingPolicy(cfg.EditingPolicy()),
		projection.WithMountSession(mountSession),
		projection.WithNamespace(cfg.Namespace),
	}
	if cfg.ReadOnly {
		fsopsOpts = append(fsopsOpts, projection.WithReadOnly())
	}
	fsops, err := projection.NewFSOps(view, fsopsOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: build fsops: %v\n", err)
		return 1
	}

	// Build the routing config snapshot once: the dispatcher does
	// not need any of the other Config fields.
	routingCfg := projection.RoutingConfig{
		ServicePrefix:   cfg.ServicePrefix,
		RootView:        cfg.RootView,
		ShowStats:       cfg.ShowStats,
		ShowByArtifact:  cfg.ShowByArtifact,
		ShowOrphaned:    cfg.ShowOrphaned,
		ShowByDate:      cfg.ShowByDate,
		ShowBySession:   cfg.ShowBySession,
		ShowByNamespace: cfg.ShowByNamespace,
		ShowRaw:         cfg.ShowRaw,
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
		if cap, err := store.Capacity(capCtx); err == nil {
			capPtr = &cap
		}
		exts := make([]projection.ExtensionInfo, 0)
		for _, e := range idx.ListExtensions() {
			exts = append(exts, projection.ExtensionInfo{
				Name:          e.Name,
				SchemaVersion: e.SchemaVersion,
			})
		}
		return projection.RenderStats(view, projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: mountSession,
			StorePath:    cfg.StorePath,
			ReadOnly:     cfg.ReadOnly,
			Namespace:    cfg.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}

	root := &rootNode{
		view:          view,
		fsops:         fsops,
		store:         store,
		routingCfg:    routingCfg,
		startedAt:     startedAt,
		statsProvider: statsProvider,
	}

	mountOpts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: cfg.AllowOther,
			Name:       "scrinium",
			FsName:     cfg.StorePath,
		},
	}

	server, err := fs.Mount(cfg.MountPoint, root, mountOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: mount: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Mounted at %s\n", cfg.MountPoint)

	// SIGINT/SIGTERM trigger a graceful unmount. fs.Server.Wait
	// blocks until either the server exits on its own or we tell
	// it to via Unmount.
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

// clearScratch removes every entry inside dir without removing
// dir itself. Errors are swallowed: scratch eviction is a
// best-effort hygiene step, not a correctness barrier.
func clearScratch(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// inodeFor maps a (tree, subPath) pair to a deterministic 64-bit
// inode number. fnv-64 is more than enough for the cardinalities
// of a single mount session; collisions translate to "two virtual
// paths share an inode", which go-fuse tolerates in practice
// because it dedupes on (parent, name) at lookup time.
//
// inode 1 is reserved for the mount root (FUSE convention).
// Service-root and stats nodes have fixed values to keep their
// inodes stable across remounts — useful for tools that cache
// `stat` output.
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

// joinTreePath glues a tree subpath together. parentSub is the
// parent path within the tree ("" = tree root), child is the
// last segment to append.
func joinTreePath(parentSub, child string) string {
	if parentSub == "" {
		return child
	}
	if child == "" {
		return parentSub
	}
	return parentSub + "/" + child
}

// strip extra trailing slash if any (defensive — go-fuse passes
// names without slashes, but we want robustness against future
// callers).
func cleanName(s string) string {
	return strings.Trim(s, "/")
}

// defaultHashRegistry returns a HashRegistry with the algorithms
// the daemon needs to read a Scrinium store written by reference
// hosts. Currently sha256 — the default content hasher in
// core.InitStore for unsealed stores.
func defaultHashRegistry() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

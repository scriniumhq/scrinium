//go:build linux || darwin

// Command scrinium-fuse mounts a Scrinium store as a POSIX-shaped
// filesystem via FUSE (Linux/macOS only; Windows users run
// scrinium-webdav).
//
// The store/projection is described by a composer config document; the
// mount point and FUSE options are flags. The config says WHAT is
// stored and how it is projected; the daemon decides WHERE and HOW to
// mount it.
//
//	scrinium-fuse mount   --config store.yaml --mount-point /mnt/scrinium
//	scrinium-fuse unmount --mount-point /mnt/scrinium
//
// This file is a reference implementation: small and self-contained,
// meant to be copied and adapted. The reusable parts live in composer
// (assembly) and engine/projection; the FUSE node tree (node.go) and
// this glue are yours to own.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"scrinium.dev"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/projection"
)

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mount":
		os.Exit(runMount(os.Args[2:]))
	case "unmount":
		os.Exit(runUnmount(os.Args[2:]))
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "scrinium-fuse: unknown command %q\n\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func runMount(args []string) int {
	fset := flag.NewFlagSet("mount", flag.ContinueOnError)
	fset.SetOutput(os.Stderr)
	configPath := fset.String("config", "", "Path to a composer YAML config file (required).")
	mountPoint := fset.String("mount-point", "", "Directory to mount onto (required).")
	allowOther := fset.Bool("allow-other", false, "Allow other users to access the mount (needs user_allow_other).")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "scrinium-fuse mount: --config is required")
		return 2
	}
	if *mountPoint == "" {
		fmt.Fprintln(os.Stderr, "scrinium-fuse mount: --mount-point is required")
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse mount: read config: %v\n", err)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	asm, err := scrinium.LoadOrInitYAML(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: %v\n", err)
		return 1
	}
	defer asm.Close()

	if asm.Projection == nil {
		fmt.Fprintln(os.Stderr, "scrinium-fuse: config has no projection section; nothing to mount")
		return 1
	}

	startedAt := time.Now().UTC()
	// FUSE is a desktop browse target: every service tree is on, rooted
	// at by-path under the _scrinium prefix.
	routingCfg := projection.RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        projection.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         true,
	}
	root := &rootNode{
		view:          asm.Projection.View,
		fsops:         asm.Projection.FSOps,
		store:         asm.Store,
		routingCfg:    routingCfg,
		startedAt:     startedAt,
		statsProvider: statsProvider(asm, startedAt, 2*time.Second),
	}

	mountOpts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: *allowOther,
			Name:       "scrinium",
			FsName:     asm.Info.StoreURI,
		},
	}

	server, err := fs.Mount(*mountPoint, root, mountOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: mount %s: %v\n", *mountPoint, err)
		return 1
	}
	go func() {
		<-ctx.Done()
		_ = server.Unmount()
	}()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", asm.MountSession)
	fmt.Fprintf(os.Stderr, "Mounted at %s\n", *mountPoint)
	server.Wait()
	return 0
}

// statsProvider renders the assembly's stats snapshot for the
// _scrinium/stats pseudo-file. capacityTimeout caps Store.Capacity so a
// slow driver never hangs a stats read; on error capacity is omitted.
func statsProvider(asm *scrinium.Scrinium, startedAt time.Time, capacityTimeout time.Duration) func() []byte {
	return func() []byte {
		capCtx, cancel := context.WithTimeout(context.Background(), capacityTimeout)
		defer cancel()

		var capPtr *domain.StorageInfo
		if info, err := asm.Store.Capacity(capCtx); err == nil {
			capPtr = &info
		}

		exts := make([]projection.ExtensionInfo, 0)
		if lister, ok := asm.Index().(index.ExtensionLister); ok {
			for _, e := range lister.ListExtensions() {
				exts = append(exts, projection.ExtensionInfo{Name: e.Name, SchemaVersion: e.SchemaVersion})
			}
		}

		meta := asm.Info
		return projection.RenderStats(asm.Projection.View, projection.DaemonInfo{
			StartedAt:    startedAt,
			MountSession: asm.MountSession,
			StorePath:    meta.StoreURI,
			ReadOnly:     meta.ReadOnly,
			Editing:      meta.Editing,
			Namespace:    meta.Namespace,
			Capacity:     capPtr,
			Extensions:   exts,
		})
	}
}

const usageText = `scrinium-fuse — mount a Scrinium store as a filesystem.

Usage:
  scrinium-fuse mount   --config <file> --mount-point <path> [--allow-other]
  scrinium-fuse unmount --mount-point <path>

The config describes the store and projection (a composer document).
Mount options are flags.

Specification: docs/4 §14 FUSE Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

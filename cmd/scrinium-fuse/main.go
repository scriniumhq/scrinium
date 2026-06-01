//go:build linux || darwin

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	pnode "scrinium.dev/projection/node"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"scrinium.dev"

	// Built-in backends register by blank import (ADR-63).
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"

	"scrinium.dev/cmd/internal/daemon"
	"scrinium.dev/projection"
	"scrinium.dev/projection/vfs"
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
	configPath := fset.String("config", "", "Path to a Scrinium YAML configuration file (required).")
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
		RootView:        pnode.RootByPath,
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         true,
	}
	fsys := vfs.New(
		asm.Projection.View,
		asm.Projection.FSOps,
		routingCfg,
		vfs.WithStatsProvider(daemon.StatsProvider(asm, startedAt, 2*time.Second)),
	)
	root := newRoot(fsys, startedAt)

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

const usageText = `scrinium-fuse — mount a Scrinium store as a filesystem.

Usage:
  scrinium-fuse mount   --config <file> --mount-point <path> [--allow-other]
  scrinium-fuse unmount --mount-point <path>

The config describes the store and projection.
Mount options are flags.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

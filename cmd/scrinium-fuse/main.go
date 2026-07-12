//go:build linux || darwin

package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	// Built-in backends register by blank import (ADR-63).
	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"

	"scrinium.dev/cmd/internal/clog"
	"scrinium.dev/cmd/internal/daemon"
	"scrinium.dev/projection/vfs"
)

const name = "scrinium-fuse"

func main() {
	os.Exit(daemon.Dispatch(name, usageText, map[string]daemon.Command{
		"mount":   runMount,
		"unmount": runUnmount,
	}))
}

func runMount(args []string) int {
	fset := flag.NewFlagSet("mount", flag.ContinueOnError)
	fset.SetOutput(os.Stderr)
	configPath := fset.String("config", "", "Path to a Scrinium YAML configuration file (required).")
	mountPoint := fset.String("mount-point", "", "Directory to mount onto (required).")
	allowOther := fset.Bool("allow-other", false, "Allow other users to access the mount (needs user_allow_other).")
	debug := fset.Bool("debug", clog.EnvDebug(), "Log every mutation (create/unlink/rename/…) with result; off shows only errors.")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if *mountPoint == "" {
		fmt.Fprintf(os.Stderr, "%s mount: --mount-point is required\n", name)
		return 2
	}

	asm, ctx, stop, code := daemon.Load(name, *configPath, true)
	if code != 0 {
		return code
	}
	defer stop()

	log := clog.New(*debug)
	startedAt := time.Now().UTC()
	// FUSE is a desktop browse target: every service tree is on, rooted
	// at by-path under the _scrinium prefix.
	routingCfg := vfs.Config{
		ServicePrefix:     "_scrinium",
		ShowStats:         true,
		ShowByArtifact:    true,
		ShowOrphaned:      true,
		ShowByDate:        true,
		ShowBySession:     true,
		ShowProvidedViews: true,
		ShowRaw:           true,
	}
	fsys := vfs.New(
		asm.Projection,
		routingCfg,
		vfs.WithStatsProvider(daemon.StatsProvider(asm, startedAt, 2*time.Second)),
	)
	root := newRoot(fsys, startedAt, log, asm.MaintenanceMode)

	mountOpts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: *allowOther,
			Name:       "scrinium",
			FsName:     asm.Info.StoreURI,
		},
	}

	server, err := fs.Mount(*mountPoint, root, mountOpts)
	if err != nil {
		log.Error("mount failed", "mount_point", *mountPoint, "err", err)
		return 1
	}
	go func() {
		<-ctx.Done()
		_ = server.Unmount()
	}()

	log.Info("mounted", "mount_point", *mountPoint)
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

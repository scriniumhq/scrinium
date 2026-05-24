//go:build linux || darwin

// Command scrinium-fuse mounts a Scrinium store as a POSIX-shaped
// filesystem via FUSE (Linux/macOS only; Windows users run
// scrinium-webdav).
//
// Since R11 the daemon is a thin loader over composer:
//
//	scrinium-fuse mount   --config /etc/scrinium/fuse.yaml
//	scrinium-fuse unmount --mount-point /mnt/scrinium
//
// The config is a composer document with a fuse surface, e.g.:
//
//	store:
//	  driver: file:///var/lib/scrinium
//	projection:
//	  namespace: files
//	surfaces:
//	  - kind: fuse
//	    config:
//	      mountPoint: /mnt/scrinium
//	      allowOther: false
//
// See docs/4 §14 FUSE Mount for the full specification.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"scrinium.dev/composer"

	_ "scrinium.dev/engine/surface/fuse"
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
	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to a composer YAML config file (required).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "scrinium-fuse mount: --config is required")
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse mount: read config: %v\n", err)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rt, err := composer.LoadOrInitYAML(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: %v\n", err)
		return 1
	}
	defer rt.Close()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", rt.MountSession())
	if err := rt.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse: %v\n", err)
		return 1
	}
	return 0
}

const usageText = `scrinium-fuse — mount a Scrinium store as a filesystem.

Usage:
  scrinium-fuse mount   --config <file>
  scrinium-fuse unmount --mount-point <path>

Commands:
  mount     Load a composer config and serve its surfaces.
  unmount   Detach a previously mounted point.
  help      Show this help message.

Specification: docs/4 §14 FUSE Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

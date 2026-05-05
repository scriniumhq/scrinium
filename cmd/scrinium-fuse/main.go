// Command scrinium-fuse mounts a Scrinium store as a POSIX-shaped
// filesystem via FUSE. The actual mount logic lives in
// mount_fuse.go behind the `fuse` build tag; without the tag the
// binary still parses configuration and exits with a clear
// "FUSE not supported in this build" message.
//
// Subcommands:
//
//	scrinium-fuse mount   --store-path=... --mount-point=... [flags]
//	scrinium-fuse unmount --mount-point=...
//
// See docs/4 §14 FUSE Mount for the full specification.
package main

import (
	"fmt"
	"os"
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

const usageText = `scrinium-fuse — mount a Scrinium store as a filesystem.

Usage:
  scrinium-fuse <command> [flags]

Commands:
  mount     Mount a store at a chosen point.
  unmount   Detach a previously mounted point.
  help      Show this help message.

Run "scrinium-fuse <command> --help" for command-specific flags.

Specification: docs/4 §14 FUSE Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}

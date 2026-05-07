//go:build !fuse || windows

package main

import (
	"fmt"
	"os"
)

// runMount is the entry point for "scrinium-fuse mount". Without
// the `fuse` build tag (or on Windows), this stub validates the
// inputs and exits with a clear "not supported" diagnostic.
//
// The real implementation lives in mount_fuse.go.
func runMount(args []string) int {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse mount: %v\n", err)
		return 2
	}
	fmt.Fprintf(os.Stderr,
		"scrinium-fuse: this build has no FUSE backend. Rebuild with `-tags fuse` (Linux/macOS only).\n"+
			"Validated config OK: store=%q mount=%q rootView=%q editing=%q.\n",
		cfg.Daemon.Store, cfg.MountPoint, cfg.Daemon.RootView, cfg.Daemon.Editing)
	return 1
}

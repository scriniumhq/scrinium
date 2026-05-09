//go:build linux || darwin

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// runUnmount is the entry point for "scrinium-fuse unmount". It
// is build-tag-agnostic — unmount is just a fork+exec around the
// platform's userland unmount tool, no FUSE library needed.
func runUnmount(args []string) int {
	fs := newUnmountFlagSet()
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mountPoint := fs.Lookup("mount-point").Value.String()
	if mountPoint == "" {
		fmt.Fprintln(os.Stderr, "scrinium-fuse unmount: --mount-point is required")
		return 2
	}
	if err := unmount(mountPoint); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-fuse unmount: %v\n", err)
		return 1
	}
	return 0
}

// newUnmountFlagSet builds the flag set for "scrinium-fuse
// unmount". Defined as a free function so tests can construct it
// without going through main().
func newUnmountFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("unmount", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.String("mount-point", "", "Mount point to detach (required).")
	return fs
}

// unmount detaches a previously mounted FUSE filesystem at
// mountPoint. It picks the platform-appropriate command:
//
//   - Linux: fusermount -u
//   - macOS: umount
//   - other: returns an error.
//
// The function is also called from runMount's cleanup path on
// SIGINT/SIGTERM, so it is intentionally idempotent: a not-mounted
// point exits with non-zero from the system tool, which we wrap
// as a clear error.
func unmount(mountPoint string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("fusermount", "-u", mountPoint)
	case "darwin":
		cmd = exec.Command("umount", mountPoint)
	default:
		return fmt.Errorf("unmount not implemented for %s", runtime.GOOS)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, out)
	}
	return nil
}

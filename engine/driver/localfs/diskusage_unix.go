//go:build unix

package localfs

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// DiskUsage implements driver.CapacityReporter via statfs on the store root,
// reporting the backing volume's total bytes and the space available to an
// unprivileged process (Bavail) — what an operator reads as "room left".
func (d *Driver) DiskUsage(ctx context.Context) (total, available int64, err error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(d.root, &st); err != nil {
		return 0, 0, fmt.Errorf("localfs: statfs %q: %w", d.root, err)
	}
	bsize := int64(st.Bsize) // int64 on linux, uint32 on darwin — widen
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil
}

package fsops

import (
	"fmt"
	"sync"

	"scrinium.dev/errs"
)

// quotaTracker enforces a global cap on the total bytes held by
// all live scratch files of an Ops instance. Reserve grows the
// counter; Release shrinks it. quota == 0 disables the cap.
type quotaTracker struct {
	mu    sync.Mutex
	used  int64
	quota int64
}

// Reserve raises the counter by n. Returns ErrScratchQuota if
// quota is enabled and the new total would exceed it. Negative n
// is treated as zero (no quota effect; matches WriteAt which
// passes max(0, growth)).
func (q *quotaTracker) Reserve(n int64) error {
	if n <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.quota > 0 && q.used+n > q.quota {
		return fmt.Errorf("%w: requested %d, used %d, cap %d",
			errs.ErrScratchQuota, n, q.used, q.quota)
	}
	q.used += n
	return nil
}

// Release shrinks the counter by n. Bottoms at zero (defensive —
// double-release should not corrupt accounting).
func (q *quotaTracker) Release(n int64) {
	if n <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used -= n
	if q.used < 0 {
		q.used = 0
	}
}

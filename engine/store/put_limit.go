package store

import (
	"fmt"
	"io"

	"scrinium.dev/errs"
)

// limitGuard enforces StoreConfig.MaxArtifactSize (class II governance,
// ADR-110) as a streaming check: Artifact carries no declared size, so
// the only honest enforcement point is the byte flow itself. The guard
// wraps the payload before the pipeline; crossing the limit aborts the
// read with errs.ErrArtifactTooLarge, Materialize fails, and nothing
// persists. Exactly limit bytes are allowed; limit+1 is refused.
type limitGuard struct {
	r     io.Reader
	left  int64 // bytes still allowed
	limit int64 // original limit, for the error message
}

func newLimitGuard(r io.Reader, limit int64) io.Reader {
	return &limitGuard{r: r, left: limit, limit: limit}
}

func (g *limitGuard) Read(p []byte) (int, error) {
	if g.left < 0 {
		return 0, fmt.Errorf("%w: limit %d bytes", errs.ErrArtifactTooLarge, g.limit)
	}
	// Read up to one byte PAST the remaining budget: the only way to
	// distinguish "exactly at the limit" from "over it" on a stream.
	max := g.left + 1
	if int64(len(p)) > max {
		p = p[:max]
	}
	n, err := g.r.Read(p)
	g.left -= int64(n)
	if g.left < 0 {
		return n, fmt.Errorf("%w: limit %d bytes", errs.ErrArtifactTooLarge, g.limit)
	}
	return n, err
}

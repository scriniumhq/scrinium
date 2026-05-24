package domain

import (
	"context"
	"io"
)

// ReadHandle is the read primitive returned by Get. It hides the
// physical source of the bytes (a single file, a HostStorage record,
// a range read from a .pack volume, or an inline blob). Support for
// ReadAt/ReadAtCtx is reported by SupportsRandomAccess; outside of
// those conditions the calls return ErrRandomAccessNotSupported.
type ReadHandle interface {
	io.ReadCloser
	io.ReaderAt

	// SupportsRandomAccess reports the static availability of
	// ReadAt/ReadAtCtx. It depends on the source's physics and the
	// composition of the Pipeline.
	SupportsRandomAccess() bool

	// ReadAtCtx is the same as ReadAt but takes an explicit
	// cancellation context. Used with network drivers, slow media,
	// and operations that require an external timeout.
	ReadAtCtx(ctx context.Context, p []byte, off int64) (n int, err error)

	// Manifest returns the parsed manifest of the artifact. Available
	// immediately after Get, before the first Read. It does not block
	// or perform I/O.
	Manifest() Manifest
}

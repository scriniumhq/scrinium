package errs

import (
	"errors"
	"io/fs"
)

// Driver / I/O surface: things that come out of the transport
// layer (driver/) or describe contracts at that layer.

// ErrUnsupportedURIScheme — the driver does not support the URI
// scheme passed to Open (Native locations / driver.Open dispatch).
var ErrUnsupportedURIScheme = errors.New("scrinium: unsupported URI scheme")

// ErrAlreadyExists — an exclusive Put (WithExclusive) found the path
// already populated and refused to overwrite it. The generic
// "already exists" at the I/O layer; bridges fs.ErrExist so callers
// can match it with errors.Is(err, fs.ErrExist). LocalFS raises it
// from the O_EXCL link; S3 from a failed If-None-Match precondition.
var ErrAlreadyExists = newBridgedSentinel(
	"scrinium: already exists", fs.ErrExist,
)

// ErrRandomAccessNotSupported — ReadAt/ReadAtCtx was called on a
// stream that does not support random access.
var ErrRandomAccessNotSupported = errors.New("scrinium: random access not supported")

package errs

import "errors"

// Driver / I/O surface: things that come out of the transport
// layer (driver/) or describe contracts at that layer.

// ErrUnsupportedURIScheme — the driver does not support the URI
// scheme passed to Open. Used with BlobStorage: ExternalRef for
// schemes unknown to the driver.
var ErrUnsupportedURIScheme = errors.New("scrinium: unsupported URI scheme")

// ErrRandomAccessNotSupported — ReadAt/ReadAtCtx was called on a
// stream that does not support random access.
var ErrRandomAccessNotSupported = errors.New("scrinium: random access not supported")

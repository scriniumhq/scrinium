package driver

import (
	"context"
	"errors"
	"io"
	"time"
)

// FileInfo is the file metadata returned by Stat. The minimal set
// sufficient for existence, size, and modification-time checks.
type FileInfo struct {
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// ObjectMeta is the extended object metadata used during iteration
// via ListObjectsWithModTime. The ETag field is optional: S3 fills
// it in, LocalFS leaves it empty.
type ObjectMeta struct {
	Path         string
	Size         int64
	LastModified time.Time
	ETag         string
}

// Driver is a stateless adapter for a single Location. It translates
// the unified set of operations into the concrete backend's API.
// One Location is served by exactly one Driver; attaching the same
// Location through two different Drivers is architecturally forbidden.
//
// Put atomicity: a Get(path) running in parallel with Put never
// observes a partially written file — it sees either the previous
// content (or os.ErrNotExist) or the new content after Put succeeds.
//
// The tombstone methods (MarkTombstone, IsTombstone) are mandatory
// for supporting Two-Phase Deletion in a multi-host environment.
type Driver interface {
	// I/O.
	Put(ctx context.Context, path string, r io.Reader) error
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	ReadAt(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error)
	Open(ctx context.Context, uri string) (io.ReadCloser, error)
	Remove(ctx context.Context, path string) error
	Rename(ctx context.Context, src, dst string) error
	Clone(ctx context.Context, src, dst string) error

	// Introspection.
	Stat(ctx context.Context, path string) (FileInfo, error)
	List(ctx context.Context, prefix string) ([]string, error)
	ListObjectsWithModTime(ctx context.Context, prefix string, since time.Time, cb func(ObjectMeta) error) error
	CountObjects(ctx context.Context, prefix string) (int64, error)

	// Maintenance.
	PruneEmptyDirs(ctx context.Context, root string) error
	Capabilities() CapabilityMask

	// Tombstone mechanics.
	MarkTombstone(ctx context.Context, path string) error
	IsTombstone(ctx context.Context, path string) (bool, error)
}

// ErrUnsupportedURIScheme indicates that the driver does not support
// the URI scheme passed to Open. Used with BlobStorage: ExternalRef
// for schemes unknown to the driver.
//
// This sentinel is also exported from core (see
// core.ErrUnsupportedURIScheme) so that the same identifier is
// available on both layers.
var ErrUnsupportedURIScheme = errors.New("driver: unsupported URI scheme")

// ErrStopWalk is the sentinel for an early but successful exit from
// ListObjectsWithModTime. Returning this value from the callback
// stops the walk without an error — the function returns nil to its
// caller.
var ErrStopWalk = errors.New("driver: stop walk")

package fsops

import (
	"context"
	"io"
	"iter"
	"time"

	"scrinium.dev/domain"
)

// StoreClient is the write-side surface Ops depends on. Defined
// here rather than reusing store.Store so that:
//
//   - the dependency is minimal — Ops does not need namespace
//     enumeration, lifecycle, crypto admin, or any of core's
//     other surface;
//   - tests can supply a fake without implementing every method
//     of store.Store.
//
// store.Store satisfies this interface naturally (subset typing
// in Go).
type StoreClient interface {
	Put(ctx context.Context, a domain.Artifact, opts ...domain.PutOption) (domain.ArtifactID, error)
	Delete(ctx context.Context, id domain.ArtifactID) error
	Get(ctx context.Context, id domain.ArtifactID, opts ...domain.GetOption) (domain.ReadHandle, error)
}

// FileInfo is the POSIX-shaped descriptor that Stat/Listdir
// returns. Built from FilesystemFacet plus Ops defaults.
type FileInfo struct {
	Name    string
	Path    string
	Size    int64
	Mode    uint32
	UID     uint32
	GID     uint32
	ModTime time.Time
	IsDir   bool

	// ArtifactID is the underlying artifact's id when the
	// FileInfo describes a file backed by a real artifact.
	// Empty for virtual directories and synthetic entries
	// that have no artifact identity. Surfaced by the web
	// browser to build info-links into the artifact details
	// page; ignored by FUSE/WebDAV which don't need it.
	ArtifactID domain.ArtifactID

	// MIME carries the MIME type recorded in vfsmeta.MIME, if
	// any. Empty when the artifact has no vfsmeta payload or
	// the payload didn't set a MIME. Surfaced by the web
	// browser to decide whether a file is safe to advertise
	// via an inline [view] link.
	MIME string
}

// FileInfoSeq is a stream of FileInfo with optional error per
// position; mirrors NodeSeq.
type FileInfoSeq = iter.Seq2[FileInfo, error]

// Handle is the file handle returned by Open/Create. It bundles random
// I/O, sync, in-place truncate, and Close — together they cover
// what FUSE write paths need. (Named Handle, not File, to keep it
// distinct from vfs.File, the dir-aware transport facade that wraps it.)
//
// Stage 4a: only read-only handles are produced (via Open with
// OpenReadOnly). Write methods (WriteAt, Truncate, Sync) on a
// read-only handle return ErrEditingDisabled.
type Handle interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
	Sync() error
	Truncate(size int64) error
}

// OpenMode is the access mode for Open. Bit-flags: combine with
// OR (e.g. OpenReadWrite | OpenAppend).
type OpenMode int

// Attrs is the set of attribute updates passed to Setattr. nil
// fields mean "leave unchanged".
type Attrs struct {
	Mode    *uint32
	UID     *uint32
	GID     *uint32
	ModTime *time.Time
}

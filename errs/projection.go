package errs

import "errors"

// Projection: virtual read-only views over a DataStore. Path-level
// errors (resolution, type mismatches) and build-tag gates for the
// optional FUSE / WebDAV mounts.

// ErrViewClosed — operation on a closed projection View.
var ErrViewClosed = errors.New("scrinium: projection view closed")

// ErrPathNotFound — Get/Open at a non-existent virtual path.
var ErrPathNotFound = errors.New("scrinium: projection path not found")

// ErrNotADirectory — List on a path that points to a file.
var ErrNotADirectory = errors.New("scrinium: projection not a directory")

// ErrIsADirectory — Open on a path that points to a directory.
var ErrIsADirectory = errors.New("scrinium: projection is a directory")

// ErrInvalidPath — the path is malformed (forbidden characters,
// absolute when relative is required, etc.).
var ErrInvalidPath = errors.New("scrinium: projection invalid path")

// ErrFUSENotSupported — MountFUSE called without the `fuse` build
// tag.
var ErrFUSENotSupported = errors.New("scrinium: FUSE not supported in this build")

// ErrWebDAVNotSupported — MountWebDAV called without the `webdav`
// build tag.
var ErrWebDAVNotSupported = errors.New("scrinium: WebDAV not supported in this build")

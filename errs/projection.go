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

// ErrSourceUnavailable — the projection source returned a transient
// error during backfill or a runtime call. The original error is
// available via errors.Unwrap.
var ErrSourceUnavailable = errors.New("scrinium: projection source unavailable")

// ErrArtifactUnreadable — the artifact is physically present but
// cannot be read in the current state (Store Locked, Corrupted,
// pipeline misconfiguration). Original error via errors.Unwrap.
var ErrArtifactUnreadable = errors.New("scrinium: projection artifact unreadable")

// ErrEditingDisabled — an editing operation (rename, setattr,
// truncate, append) was attempted while the corresponding policy
// bit is off, or any mutation was attempted on a read-only FSOps.
// Transports translate to EROFS (FUSE) or 403 (WebDAV).
var ErrEditingDisabled = errors.New("scrinium: projection editing disabled")

// ErrScratchQuota — FSOps.Create/Write would exceed the configured
// scratch quota. Translated to ENOSPC at the FUSE layer.
var ErrScratchQuota = errors.New("scrinium: projection scratch quota exceeded")

// ErrPathExists — Create/Mkdir at a path that is already taken
// (real artifact or pending directory). Translates to EEXIST at
// the FUSE layer.
var ErrPathExists = errors.New("scrinium: projection path already exists")

// ErrNotEmpty — Rmdir on a directory that has children. Translates
// to ENOTEMPTY at the FUSE layer.
var ErrNotEmpty = errors.New("scrinium: projection directory not empty")

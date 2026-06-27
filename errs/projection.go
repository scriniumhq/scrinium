package errs

import (
	"errors"
	"io/fs"
)

// Projection: virtual read-only views over a DataStore. Path-level
// errors (resolution, type mismatches) and build-tag gates for the
// optional FUSE / WebDAV mounts.
//
// Closed-view operations surface the standard-library os.ErrClosed;
// callers that need to detect a closed View should errors.Is against
// os.ErrClosed (which is what View returns).

// ErrPathNotFound — Get/Open at a non-existent virtual path.
// Bridges to fs.ErrNotExist so host code can errors.Is against
// the standard-library sentinel without knowing scrinium specifics.
var ErrPathNotFound = newBridgedSentinel(
	"scrinium: projection path not found", fs.ErrNotExist,
)

// ErrNotADirectory — List on a path that points to a file.
// Bridges to fs.ErrInvalid (mirrors the vfs.WrapErr
// translation; surfaces map fs.ErrInvalid to ENOTDIR themselves).
var ErrNotADirectory = newBridgedSentinel(
	"scrinium: projection not a directory", fs.ErrInvalid,
)

// ErrIsADirectory — Open on a path that points to a directory.
// Bridges to fs.ErrInvalid for the same reason as ErrNotADirectory.
var ErrIsADirectory = newBridgedSentinel(
	"scrinium: projection is a directory", fs.ErrInvalid,
)

// ErrInvalidPath — the path is malformed (forbidden characters,
// absolute when relative is required, etc.).
var ErrInvalidPath = newBridgedSentinel(
	"scrinium: projection invalid path", fs.ErrInvalid,
)

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
// Bridges to fs.ErrPermission; surfaces translate to EROFS (FUSE)
// or 403 (WebDAV).
var ErrEditingDisabled = newBridgedSentinel(
	"scrinium: projection editing disabled", fs.ErrPermission,
)

// ErrScratchQuota — FSOps.Create/Write would exceed the configured
// scratch quota. Bridges to fs.ErrPermission (host surfaces map it
// to ENOSPC); the bridge keeps "is it permission-class?" answers
// uniform across editing-policy and quota refusals.
var ErrScratchQuota = newBridgedSentinel(
	"scrinium: projection scratch quota exceeded", fs.ErrPermission,
)

// ErrPathExists — Create/Mkdir at a path that is already taken
// (real artifact or pending directory). Bridges to fs.ErrExist;
// translates to EEXIST at the FUSE layer.
var ErrPathExists = newBridgedSentinel(
	"scrinium: projection path already exists", fs.ErrExist,
)

// ErrNotEmpty — Rmdir on a directory that has children. Bridges
// to fs.ErrInvalid (mirrors vfs.WrapErr); translates to ENOTEMPTY
// at the FUSE layer.
var ErrNotEmpty = newBridgedSentinel(
	"scrinium: projection directory not empty", fs.ErrInvalid,
)

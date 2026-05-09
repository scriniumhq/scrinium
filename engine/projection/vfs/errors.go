package vfs

import (
	"errors"
	"io/fs"

	"scrinium.dev/engine/errs"
)

// WrapErr translates a projection-level error into the
// io/fs sentinel surfaces typically expect. Returns the
// original error for unrecognised classes — callers can
// errors.Is() against either layer.
//
// Surfaces (FUSE, WebDAV) often translate fs.* errors into
// platform errnos themselves (syscall.ENOENT, etc.); going
// through fs.* gives them a uniform pivot.
func WrapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errs.ErrPathNotFound):
		return fs.ErrNotExist
	case errors.Is(err, errs.ErrPathExists):
		return fs.ErrExist
	case errors.Is(err, errs.ErrIsADirectory),
		errors.Is(err, errs.ErrNotADirectory),
		errors.Is(err, errs.ErrNotEmpty),
		errors.Is(err, errs.ErrInvalidPath):
		return fs.ErrInvalid
	case errors.Is(err, errs.ErrEditingDisabled),
		errors.Is(err, errs.ErrScratchQuota):
		return fs.ErrPermission
	}
	return err
}

// WrapErrno produces an fs.*-class error directly from a
// projection sentinel without wrapping in a fmt.Errorf.
// Used by call sites that synthesise a sentinel themselves
// (e.g. write to a service tree → ErrEditingDisabled →
// fs.ErrPermission).
func WrapErrno(err error) error {
	switch {
	case errors.Is(err, errs.ErrPathNotFound):
		return fs.ErrNotExist
	case errors.Is(err, errs.ErrPathExists):
		return fs.ErrExist
	case errors.Is(err, errs.ErrEditingDisabled):
		return fs.ErrPermission
	}
	return err
}

package vfs

import (
	"context"
	"errors"
	"os"
	fso "scrinium.dev/projection/internal/fsops"

	"scrinium.dev/errs"
)

// openRoot is the FSOps-backed side of OpenFile.
func (v *VFS) openRoot(
	ctx context.Context,
	subPath string,
	flag int,
	perm os.FileMode,
	wantCreate, wantWrite bool,
) (File, error) {
	// Empty subPath = mount root; always treat as readable
	// directory.
	if subPath == "" {
		return newRootDirFile(v), nil
	}
	if wantCreate {
		// O_CREAT semantics: try to create; on EEXIST fall
		// through to open if O_EXCL was not set.
		f, err := v.fsops.Create(ctx, subPath, uint32(perm))
		if err == nil {
			return wrapWriteFile(f, subPath), nil
		}
		if !errors.Is(err, errs.ErrPathExists) || flag&syscallOExcl != 0 {
			return nil, err
		}
		// EEXIST without O_EXCL — fall through to open below.
	}
	// Stat first to decide file vs dir.
	fi, err := v.fsops.Stat(subPath)
	if err != nil {
		return nil, err
	}
	if fi.IsDir {
		// Read-only dir handle for Readdir.
		return newPathDirFile(v, subPath), nil
	}
	mode := fso.OpenReadOnly
	if wantWrite {
		mode = fso.OpenReadWrite
	}
	f, err := v.fsops.Open(ctx, subPath, mode)
	if err != nil {
		return nil, err
	}
	return wrapFile(f, subPath, fi), nil
}

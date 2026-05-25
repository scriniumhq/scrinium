package vfs

import (
	"context"
	"errors"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/engine/projection"
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
	mode := projection.OpenReadOnly
	if wantWrite {
		mode = projection.OpenReadWrite
	}
	f, err := v.fsops.Open(ctx, subPath, mode)
	if err != nil {
		return nil, err
	}
	return wrapFile(f, subPath, fi), nil
}

// serviceLookup dispatches a Get on the right tree.
func serviceLookup(view *projection.View, tree projection.RootView, sub string) (projection.Node, error) {
	switch tree {
	case projection.RootByPath:
		return view.GetByPath(sub)
	case projection.RootBySession:
		return view.GetBySession(sub)
	case projection.RootByNamespace:
		return view.GetByNamespace(sub)
	case projection.RootByDate:
		return view.GetByDate(sub)
	case projection.RootByArtifact:
		return view.GetByArtifact(sub)
	case projection.RootByOrphaned:
		return view.GetByOrphaned(sub)
	}
	return projection.Node{}, errs.ErrPathNotFound
}

// serviceList dispatches a List on the right tree.
func serviceList(view *projection.View, tree projection.RootView, sub string) projection.NodeSeq {
	switch tree {
	case projection.RootByPath:
		return view.ListByPath(sub)
	case projection.RootBySession:
		return view.ListBySession(sub)
	case projection.RootByNamespace:
		return view.ListByNamespace(sub)
	case projection.RootByDate:
		return view.ListByDate(sub)
	case projection.RootByArtifact:
		return view.ListByArtifact(sub)
	case projection.RootByOrphaned:
		return view.ListByOrphaned(sub)
	}
	return func(yield func(projection.Node, error) bool) {
		yield(projection.Node{}, errs.ErrPathNotFound)
	}
}

// serviceOpen dispatches an Open on the right tree.
func serviceOpen(ctx context.Context, view *projection.View, tree projection.RootView, sub string) (domain.ReadHandle, error) {
	switch tree {
	case projection.RootByPath:
		return view.OpenByPath(ctx, sub, domain.GetOptions{})
	case projection.RootBySession:
		return view.OpenBySession(ctx, sub, domain.GetOptions{})
	case projection.RootByNamespace:
		return view.OpenByNamespace(ctx, sub, domain.GetOptions{})
	case projection.RootByDate:
		return view.OpenByDate(ctx, sub, domain.GetOptions{})
	case projection.RootByArtifact:
		return view.OpenByArtifact(ctx, sub, domain.GetOptions{})
	case projection.RootByOrphaned:
		return view.OpenByOrphaned(ctx, sub, domain.GetOptions{})
	}
	return nil, errs.ErrPathNotFound
}

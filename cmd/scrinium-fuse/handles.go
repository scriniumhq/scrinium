//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"scrinium.dev/projection/vfs"
)

// vfsFileHandle adapts a vfs.FileAt to the go-fuse handle
// interfaces. It replaces the three per-source handles (root
// read/write file, read-only service-tree handle, in-memory stats
// file): vfs.OpenFileAt returns a FileAt for every route, so one
// handle covers all of them.
//
// Read-only files (service trees, stats) reject WriteAt with
// fs.ErrPermission, surfaced here as EROFS. go-fuse may dispatch
// concurrent Read/Write on the same handle, so the mutex guards
// the underlying file.
type vfsFileHandle struct {
	mu sync.Mutex
	f  vfs.FileAt
}

var (
	_ fs.FileReader   = (*vfsFileHandle)(nil)
	_ fs.FileWriter   = (*vfsFileHandle)(nil)
	_ fs.FileFlusher  = (*vfsFileHandle)(nil)
	_ fs.FileReleaser = (*vfsFileHandle)(nil)
	_ fs.FileFsyncer  = (*vfsFileHandle)(nil)
)

// newVFSFileHandle wraps an open vfs.FileAt for FUSE. The handle
// takes ownership of the file and closes it on Release.
func newVFSFileHandle(f vfs.FileAt) *vfsFileHandle {
	return &vfsFileHandle{f: f}
}

func (h *vfsFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.f.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errnoFromError(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *vfsFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.f.WriteAt(data, off)
	if err != nil {
		if errors.Is(err, iofs.ErrPermission) {
			return uint32(n), syscall.EROFS
		}
		return uint32(n), errnoFromError(err)
	}
	return uint32(n), 0
}

func (h *vfsFileHandle) Flush(ctx context.Context) syscall.Errno {
	// FUSE Flush fires on every close(2); the artifact is
	// materialised in Release, so Flush only acknowledges.
	return 0
}

func (h *vfsFileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.f.Sync(); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (h *vfsFileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.f.Close(); err != nil {
		return errnoFromError(err)
	}
	return 0
}

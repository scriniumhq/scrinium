package drivers

import (
	"context"
	"io"
	"os"
)

// FileInfo extends standard os.FileInfo for storage-specific optimizations.
type FileInfo interface {
	os.FileInfo
	// StorageHash returns the hash if the underlying storage (e.g., S3 ETag)
	// can provide it without reading the file body. Returns empty string if not available.
	StorageHash() string
}

// File defines the strict contract for an opened blob, supporting concurrent ReadAt.
type File interface {
	io.ReadSeeker
	io.ReaderAt
	io.Closer
	Size() (int64, error)
}

// GuestDriver represents a read-only or assimilate-only storage (e.g., external USB).
type GuestDriver interface {
	ID() string
	// Open returns drivers.File which includes Size() and ReadAt.
	Open(ctx context.Context, path string) (File, error)
	Walk(ctx context.Context, prefix string, fn func(FileInfo) error) error
	Delete(ctx context.Context, path string) error
}

// NativeDriver represents a fully managed CAS storage with write capabilities.
type NativeDriver interface {
	GuestDriver
	Put(ctx context.Context, path string, data io.Reader) error
	LinkOrCopy(ctx context.Context, src, dst string) error
	Move(ctx context.Context, src, dst string) error
}

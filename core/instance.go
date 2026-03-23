package core

import (
	"context"
	"io"

	"github.com/rkurbatov/scrinium/drivers"
)

// Instance is the strictly stateless gateway to a physical CAS storage.
type Instance interface {
	Put(ctx context.Context, r io.Reader) (BlobManifest, error)
	IngestLocal(ctx context.Context, physicalPath string, keepOriginal bool) (BlobManifest, func() error, error)
	Open(ctx context.Context, payloadHash string) (drivers.File, error)
	Materialize(ctx context.Context, payloadHash string, prefix, ext string) (string, func(), error)
	GetManifest(ctx context.Context, payloadHash string) (BlobManifest, error)
}

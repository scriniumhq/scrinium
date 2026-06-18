package artifact

// readhandle.go — the in-memory ReadHandle over an inline manifest's
// payload. It is addressing-agnostic (it only needs the manifest's
// InlineBlob), so it lives in the shared manifest layer and is used by
// both addressing schemes: content-addressed inline blobs
// (engine/internal/casio) and name-addressed system artifacts
// (engine/systemstore). The blob-backed and verifying handles, which
// need the driver and the inverse pipeline, stay in casio.

import (
	"bytes"
	"context"

	"scrinium.dev/domain"
)

type inlineReadHandle struct {
	manifest domain.Manifest
	reader   *bytes.Reader
}

// NewInlineHandle builds a read handle over an inline manifest's payload.
// Used for content-addressed inline blobs and for name-addressed system
// artifacts.
func NewInlineHandle(m domain.Manifest) domain.ReadHandle {
	return &inlineReadHandle{manifest: m, reader: bytes.NewReader(m.InlineBlob)}
}

func (h *inlineReadHandle) Read(p []byte) (int, error) { return h.reader.Read(p) }
func (h *inlineReadHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.reader.ReadAt(p, off)
}
func (h *inlineReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.reader.ReadAt(p, off)
}
func (h *inlineReadHandle) SupportsRandomAccess() bool { return true }
func (h *inlineReadHandle) Close() error               { return nil } // in-memory; idempotent
func (h *inlineReadHandle) Manifest() domain.Manifest  { return h.manifest }

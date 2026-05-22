package aesgcm

import (
	"crypto/cipher"
	"errors"
	"io"

	"scrinium.dev/engine/internal/segaead"
	"scrinium.dev/engine/pipeline"
)

// encoder is the per-operation pinned-DEK Encoder. It delegates the
// on-disk framing to segaead: the blob is a header plus a sequence of
// fixed-size AEAD segments, each with its own IV and tag, produced in
// a single streaming pass with O(SegmentSize) memory (ADR-59). There
// is no whole-blob io.ReadAll any more.
type encoder struct {
	gcm     cipher.AEAD
	dek     []byte // HMAC key for convergent IV derivation
	mode    segaead.IVMode
	segSize int
	keyID   string // empty for pinned-DEK

	sealed  *segaead.SealReader
	started bool
}

func (e *encoder) Transform(r io.Reader) io.Reader {
	if e.started {
		return errReader{err: errors.New("aesgcm encoder reused")}
	}
	e.started = true

	sr, err := segaead.Seal(r, segaead.SealParams{
		AEAD:        e.gcm,
		Mode:        e.mode,
		DEK:         e.dek,
		KeyID:       e.keyID,
		SegmentSize: e.segSize,
	})
	if err != nil {
		return errReader{err: err}
	}
	e.sealed = sr
	return sr
}

// Result returns the byte count produced by the framed encoder. IV
// is nil: the segmented format keeps a per-segment IV in each frame,
// so manifest.Pipeline[i].IV stays empty for this stage (ADR-59).
// KeyID is empty for the pinned-DEK factory.
func (e *encoder) Result() pipeline.TransformResult {
	var out int64
	if e.sealed != nil {
		out = e.sealed.BytesWritten()
	}
	return pipeline.TransformResult{
		OutputSize: out,
		IV:         nil,
		KeyID:      e.keyID,
	}
}

var _ pipeline.Encoder = (*encoder)(nil)

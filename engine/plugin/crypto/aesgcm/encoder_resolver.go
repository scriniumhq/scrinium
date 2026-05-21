package aesgcm

import (
	"errors"
	"io"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/internal/segaead"
)

// resolverEncoder is the per-operation Encoder for the
// resolver-backed factory. The engine chooses the write KeyID once
// (KeyResolver.ResolveWriteKey) and passes it at construction via
// EncodeContext; the encoder resolves the DEK for that KeyID on first
// Transform, builds the AES-GCM primitive, and delegates framing to
// segaead. Under Convergent the same DEK is the HMAC key for
// per-segment IV derivation, so the survivor blob is byte-reproducible
// (ADR-58/59).
type resolverEncoder struct {
	resolver coreapi.KeyResolver
	keyID    string // chosen by the engine, fixed at construction
	mode     segaead.IVMode
	segSize  int

	sealed  *segaead.SealReader
	started bool
}

func (e *resolverEncoder) Transform(r io.Reader) io.Reader {
	if e.started {
		return errReader{err: errors.New("aesgcm resolver encoder reused")}
	}
	e.started = true

	// The engine already picked the write KeyID and handed it to us
	// via EncodeContext. Fetch the DEK candidates for it and use the
	// first — the write side never tries alternatives (that is a
	// read-path concern).
	keys, err := resolveKeys(e.resolver, e.keyID)
	if err != nil {
		return errReader{err: err}
	}
	dek := keys[0]
	aead, err := buildAEAD(dek)
	if err != nil {
		return errReader{err: err}
	}

	sr, err := segaead.Seal(r, segaead.SealParams{
		AEAD:        aead,
		Mode:        e.mode,
		DEK:         dek,
		KeyID:       e.keyID,
		SegmentSize: e.segSize,
	})
	if err != nil {
		return errReader{err: err}
	}
	e.sealed = sr
	return sr
}

// Result returns the produced byte count and the KeyID this encoder
// was constructed with (copied by the runner into the manifest stage
// so the Decoder can resolve the same key on read). IV is nil — the
// segmented format keeps per-segment IVs inside the blob (ADR-59).
func (e *resolverEncoder) Result() coreapi.TransformResult {
	var out int64
	if e.sealed != nil {
		out = e.sealed.BytesWritten()
	}
	return coreapi.TransformResult{
		OutputSize: out,
		IV:         nil,
		KeyID:      e.keyID,
	}
}

var _ coreapi.Encoder = (*resolverEncoder)(nil)

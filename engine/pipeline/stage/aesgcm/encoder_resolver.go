package aesgcm

import (
	"errors"
	"io"

	aeadkit "scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/pipeline"
	segaead2 "scrinium.dev/engine/pipeline/internal/segaead"
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
	resolver pipeline.KeyResolver
	keyID    string // chosen by the engine, fixed at construction
	mode     segaead2.IVMode
	segSize  int

	sealed  *segaead2.SealReader
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
	// The write side uses only the chosen key; the remaining candidates are
	// never tried (rotation is a read-path concern). Wipe them now so unused
	// DEK copies do not linger in the heap. keys[0]/dek must survive: AES-GCM
	// and the convergent-IV HMAC hold it for the whole seal.
	for _, k := range keys[1:] {
		aeadkit.Wipe(k)
	}
	aead, err := buildAEAD(dek)
	if err != nil {
		return errReader{err: err}
	}

	sr, err := segaead2.Seal(r, segaead2.SealParams{
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
func (e *resolverEncoder) Result() pipeline.TransformResult {
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

var _ pipeline.Encoder = (*resolverEncoder)(nil)

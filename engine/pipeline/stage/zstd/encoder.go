package zstd

import (
	"errors"
	"io"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
	"scrinium.dev/engine/pipeline"
)

// encoder is the per-operation Encoder for zstd.
//
// Implementation note: Bypass heuristics described in docs §7.1
// (entropy sampling on a leading window, microsize bypass) are
// deferred — this is a straightforward streaming implementation
// that compresses every frame at the configured level. Random-
// bytes inputs end up slightly larger than their plaintext, never
// wrong. The Bypass optimisation is tracked in the backlog under
// "M2-extra: zstd encoder Bypass heuristics"; bringing it back
// will swap this file without touching the factory or the
// Decoder.
type encoder struct {
	opts Options

	outputSize atomic.Int64
	started    bool
}

func newEncoder(opts Options) *encoder {
	return &encoder{opts: opts}
}

// Transform wraps the input reader. All work happens in a
// background goroutine writing into an io.Pipe — the runner reads
// the resulting zstd frame from the returned reader. No part of
// the stream is buffered in memory: zstd encodes incrementally,
// and the pipe is bounded by the consumer's read rate.
func (e *encoder) Transform(r io.Reader) io.Reader {
	if e.started {
		// Defensive: per the contract a fresh Encoder is used for
		// each operation. Returning a closed reader makes the
		// programmer error visible at the first Read.
		pr, pw := io.Pipe()
		_ = pw.CloseWithError(errors.New("zstd encoder reused"))
		return pr
	}
	e.started = true

	pr, pw := io.Pipe()
	go func() {
		// Constructing the writer directly with pw avoids the
		// Reset(pw) trick — Reset can flush pending bytes into a
		// pipe whose reader has not yet started, which deadlocks
		// when the encoder runs ahead of the consumer.
		zw, err := zstd.NewWriter(pw, zstd.WithEncoderLevel(e.opts.Level))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err := io.Copy(zw, r); err != nil {
			_ = zw.Close()
			_ = pw.CloseWithError(err)
			return
		}
		if err := zw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	return &countingReader{r: pr, n: &e.outputSize}
}

// Result returns the recorded metrics. Called by the runner after
// EOF on the wrapped reader. Entropy is always 0 (Bypass
// heuristics deferred — see type comment).
func (e *encoder) Result() pipeline.TransformResult {
	return pipeline.TransformResult{
		OutputSize: e.outputSize.Load(),
	}
}

// countingReader counts bytes read out of an io.Reader and stores
// the running total in an atomic int64 owned by the encoder.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	return n, err
}

package core

import (
	"errors"
	"fmt"
	"hash"
	"io"

	"scrinium.dev/engine/domain"
)

// pipelineRunner builds and drives a chain of Encoder / Decoder
// stages over a streaming reader, computing per-stage output
// hashes via TeeReader. It is the single place that translates
// docs §2.3 (Data Flows) and §7.1 (Plugins) into Go.
//
// Lifecycle. A runner is per-operation: built once, drained once,
// discarded. It is not safe for concurrent use.
//
// On the write path we hash the original (pre-Encoder) and final
// (post-last-Encoder) streams — the former feeds dedup
// (ContentHash), the latter feeds disk addressing (BlobRef). Each
// intermediate stage records its own output hash via Tee.
//
// On the read path we hash nothing: the inverse pipeline simply
// produces the original bytes; integrity of the encrypted/
// compressed bytes is guaranteed at write-time (the on-disk file
// is named after BlobRef) and re-verified by the AEAD tag for
// crypto stages, per docs §3.2 "neявная верификация".

// --- Write path ---

// putPipeline holds the per-stage Encoder instances and the
// running hashers needed to assemble manifest.Pipeline after EOF.
type putPipeline struct {
	stages      []putPipelineStage
	contentHash hash.Hash // hashes the original input
	blobRefHash hash.Hash // hashes the final output
	hashAlgo    string
	inCounter   *countingReader
}

type putPipelineStage struct {
	algorithm string
	encoder   Encoder
	hasher    hash.Hash
}

// buildPutPipeline composes the Encoder chain dictated by
// algoIDs over the underlying input reader. The returned reader
// yields the bytes that should be written to disk; after it has
// been drained to EOF, call finalize() to recover ContentHash,
// BlobRef and the per-stage manifest entries.
func (s *store) buildPutPipeline(
	hashAlgo string,
	input io.Reader,
	algoIDs []string,
) (io.Reader, *putPipeline, error) {
	contentHasher, err := s.hashes.NewHasher(hashAlgo)
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: content hasher: %w", err)
	}
	blobRefHasher, err := s.hashes.NewHasher(hashAlgo)
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: blobref hasher: %w", err)
	}

	// Tee the *input* so we always observe the original bytes for
	// ContentHash, regardless of how many stages follow.
	inCounter := &countingReader{r: input}
	current := io.TeeReader(inCounter, contentHasher)

	pp := &putPipeline{
		contentHash: contentHasher,
		blobRefHash: blobRefHasher,
		hashAlgo:    hashAlgo,
		inCounter:   inCounter,
	}

	for _, algo := range algoIDs {
		factory, err := s.transformers.Get(algo)
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline: get factory %q: %w", algo, err)
		}
		enc := factory.NewEncoder()
		stageOut := enc.Transform(current)

		// Each stage gets its own hasher: per-stage hashes are
		// recorded in the manifest for diagnostic purposes (and
		// will be re-verified by Scrub in M3).
		stageHasher, err := s.hashes.NewHasher(hashAlgo)
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline: stage hasher %q: %w", algo, err)
		}
		current = io.TeeReader(stageOut, stageHasher)

		pp.stages = append(pp.stages, putPipelineStage{
			algorithm: algo,
			encoder:   enc,
			hasher:    stageHasher,
		})
	}

	// Final tee: the bytes about to hit the disk feed BlobRef.
	current = io.TeeReader(current, blobRefHasher)

	return current, pp, nil
}

// finalize collects ContentHash, BlobRef and the manifest
// pipeline entries. Must be called after the reader returned by
// buildPutPipeline has been drained to EOF.
func (pp *putPipeline) finalize(formatHash func(string, []byte) string) (
	contentHash domain.ContentHash,
	blobRef domain.BlobRef,
	stages []domain.PipelineStage,
) {
	contentHash = domain.ContentHash(
		formatHash(pp.hashAlgo, pp.contentHash.Sum(nil)))
	blobRef = domain.BlobRef(
		formatHash(pp.hashAlgo, pp.blobRefHash.Sum(nil)))

	stages = make([]domain.PipelineStage, 0, len(pp.stages))
	for _, st := range pp.stages {
		res := st.encoder.Result()
		stages = append(stages, domain.PipelineStage{
			Algorithm: st.algorithm,
			Hash:      formatHash(pp.hashAlgo, st.hasher.Sum(nil)),
			IV:        res.IV,
		})
	}
	return contentHash, blobRef, stages
}

// --- Read path ---

// buildGetReader composes the inverse Decoder chain over an
// underlying ciphertext/compressed reader. Stages are applied in
// REVERSE order: the last Encoder applied at write time is the
// first Decoder undone at read time. With an empty manifestStages
// it returns underlying as-is.
//
// The returned io.ReadCloser closes underlying when itself
// closed; intermediate Decoder readers are best-effort closed via
// runtime finalisation through io.Pipe (see plugin docs).
func (s *store) buildGetReader(
	manifestStages []domain.PipelineStage,
	underlying io.ReadCloser,
) (io.ReadCloser, error) {
	if len(manifestStages) == 0 {
		return underlying, nil
	}

	cur := io.Reader(underlying)
	for i := len(manifestStages) - 1; i >= 0; i-- {
		stage := manifestStages[i]
		factory, err := s.transformers.Get(stage.Algorithm)
		if err != nil {
			_ = underlying.Close()
			return nil, fmt.Errorf("pipeline: get factory %q: %w",
				stage.Algorithm, err)
		}
		dec := factory.NewDecoder(stage)
		cur = dec.Transform(cur)
	}

	return &decoderReadCloser{r: cur, underlying: underlying}, nil
}

// contentBytesRead returns the number of bytes read from the
// original input (the pre-Pipeline payload).
func (pp *putPipeline) contentBytesRead() int64 {
	return pp.inCounter.n
}

// decoderReadCloser composes a Reader produced by a Decoder chain
// with the Closer of the underlying source. Closing it closes the
// underlying source; the intermediate readers are streaming
// io.Pipe wrappers and exit cleanly when the underlying source
// reports EOF or error.
type decoderReadCloser struct {
	r          io.Reader
	underlying io.Closer
	closed     bool
}

func (d *decoderReadCloser) Read(p []byte) (int, error) {
	return d.r.Read(p)
}

func (d *decoderReadCloser) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	if d.underlying == nil {
		return nil
	}
	return d.underlying.Close()
}

// validatePipelineAlgos checks that every algorithm referenced in
// cfg.Pipeline is present in the registry. Returns
// errs.ErrUnsupportedAlgorithm wrapped with the missing id.
//
// Defined here rather than in put.go so that future callers (e.g.
// validateAgainstActiveConfig at OpenStore time) can share it.
func (s *store) validatePipelineAlgos(algoIDs []string) error {
	for _, algo := range algoIDs {
		if _, err := s.transformers.Get(algo); err != nil {
			return fmt.Errorf("pipeline: %q: %w", algo, err)
		}
	}
	return nil
}

// errPipelineWithInline is returned when an Inline blob would have
// to flow through a non-empty Pipeline. Inline + Pipeline is
// reserved for a later milestone (see backlog "M2-extra: Pipeline
// on inline blobs").
var errPipelineWithInline = errors.New(
	"core.Put: Pipeline transforms on Inline blobs are not supported in M2.1")

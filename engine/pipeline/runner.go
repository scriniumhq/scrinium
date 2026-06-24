package pipeline

// runner.go — the transform engine: builds and drives a chain of
// Encoder / Decoder stages over a streaming reader, computing
// per-stage output hashes via TeeReader. It is the single place that
// translates docs §2.3 (Data Flows) and §7.1 (Plugins) into Go.
//
// Extracted from store (was store/store_pipeline.go): store keeps
// policy and orchestration, pipeline owns the engine. A Runner is
// constructed per operation by store via NewRunner(hashes,
// transformers); it is a cheap struct wrapper over those two
// registries.
//
// Lifecycle. A PutPipeline is per-operation: built once (BuildPut),
// drained once, finalized once, discarded. It is not safe for
// concurrent use.
//
// On the write path we hash the original (pre-Encoder) and final
// (post-last-Encoder) streams — the former feeds dedup (ContentHash),
// the latter feeds disk addressing (BlobRef). Each intermediate stage
// records its own output hash via Tee.
//
// On the read path we hash nothing: the inverse pipeline simply
// produces the original bytes; integrity of the encrypted/compressed
// bytes is guaranteed at write-time (the on-disk file is named after
// BlobRef) and re-verified by the AEAD tag for crypto stages
// (implicit verification).

import (
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Runner builds and drives Encoder/Decoder chains over streaming
// readers, computing the three classes of hash (ContentHash on the
// original input, BlobRef on the final output, and a per-stage hash
// for diagnostics). It holds the two registries the engine needs and
// nothing else.
type Runner struct {
	hashes       domain.HashRegistry
	transformers TransformerRegistry
}

// NewRunner returns a Runner bound to the given registries. Cheap —
// store builds one per operation.
func NewRunner(hashes domain.HashRegistry, transformers TransformerRegistry) *Runner {
	return &Runner{hashes: hashes, transformers: transformers}
}

// --- Write path ---

// PutPipeline holds the per-stage Encoder instances and the running
// hashers needed to assemble manifest.Pipeline after EOF. Built by
// BuildPut; finalized by Finalize once the reader has been drained.
type PutPipeline struct {
	stages      []putPipelineStage
	contentHash hash.Hash // hashes the original input
	blobRefHash hash.Hash // hashes the final output
	hashAlgo    string
	inCounter   *countingReader
	formatHash  func(string, []byte) string // captured from hashes.Format in BuildPut
}

type putPipelineStage struct {
	algorithm string
	encoder   Encoder
	hasher    hash.Hash
}

// BuildPut composes the Encoder chain dictated by algoIDs over the
// underlying input reader. The returned reader yields the bytes that
// should be written to disk; after it has been drained to EOF, call
// Finalize() to recover ContentHash, BlobRef and the per-stage
// manifest entries.
func (r *Runner) BuildPut(
	hashAlgo string,
	input io.Reader,
	algoIDs []string,
	ec EncodeContext,
) (io.Reader, *PutPipeline, error) {
	contentHasher, err := r.hashes.NewHasher(hashAlgo)
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: content hasher: %w", err)
	}
	blobRefHasher, err := r.hashes.NewHasher(hashAlgo)
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: blobref hasher: %w", err)
	}

	// Tee the *input* so we always observe the original bytes for
	// ContentHash, regardless of how many stages follow.
	inCounter := &countingReader{r: input}
	current := io.TeeReader(inCounter, contentHasher)

	pp := &PutPipeline{
		contentHash: contentHasher,
		blobRefHash: blobRefHasher,
		hashAlgo:    hashAlgo,
		inCounter:   inCounter,
		formatHash:  r.hashes.Format,
	}

	for _, algo := range algoIDs {
		factory, err := r.transformers.Get(algo)
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline: get factory %q: %w", algo, err)
		}
		enc := factory.NewEncoder(ec)
		stageOut := enc.Transform(current)

		// Each stage gets its own hasher: per-stage hashes are
		// recorded in the manifest for diagnostic purposes (and will
		// be re-verified by Scrub in M3).
		stageHasher, err := r.hashes.NewHasher(hashAlgo)
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

// Finalize collects ContentHash, BlobRef and the manifest pipeline
// entries. Must be called after the reader returned by BuildPut has
// been drained to EOF. The hash-formatting function is the one
// captured from the registry in BuildPut.
func (pp *PutPipeline) Finalize() (
	contentHash domain.ContentHash,
	blobRef domain.BlobRef,
	stages []domain.PipelineStage,
) {
	contentHash = domain.ContentHash(
		hex.EncodeToString(pp.contentHash.Sum(nil)))
	blobRef = domain.BlobRef(
		hex.EncodeToString(pp.blobRefHash.Sum(nil)))

	stages = make([]domain.PipelineStage, 0, len(pp.stages))
	for _, st := range pp.stages {
		res := st.encoder.Result()
		stages = append(stages, domain.PipelineStage{
			Algorithm: st.algorithm,
			Hash:      pp.formatHash(pp.hashAlgo, st.hasher.Sum(nil)),
			IV:        res.IV,
			KeyID:     res.KeyID,
		})
	}
	return contentHash, blobRef, stages
}

// ContentBytesRead returns the number of bytes read from the original
// input (the pre-Pipeline payload).
func (pp *PutPipeline) ContentBytesRead() int64 {
	return pp.inCounter.n
}

// --- Read path ---

// BuildGet composes the inverse Decoder chain over an underlying
// ciphertext/compressed reader. Stages are applied in REVERSE order:
// the last Encoder applied at write time is the first Decoder undone
// at read time. With an empty manifestStages it returns underlying
// as-is.
//
// The returned io.ReadCloser closes underlying when itself closed;
// intermediate Decoder readers are best-effort closed via runtime
// finalisation through io.Pipe (see plugin docs).
func (r *Runner) BuildGet(
	manifestStages []domain.PipelineStage,
	underlying io.ReadCloser,
) (io.ReadCloser, error) {
	if len(manifestStages) == 0 {
		return underlying, nil
	}

	cur := io.Reader(underlying)
	for i := len(manifestStages) - 1; i >= 0; i-- {
		stage := manifestStages[i]
		factory, err := r.transformers.Get(stage.Algorithm)
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

// ValidateAlgos checks that the configured pipeline is legal on the
// WRITE path: every algorithm is present in the registry (else
// errs.ErrUnsupportedAlgorithm, wrapped with the missing id) AND a crypto
// (AEAD) stage is terminal (else errs.ErrInvalidPipeline). Crypto must be
// last so the on-disk bytes are the encrypted bytes
// (2. Internals/03 Cryptography).
//
// Called on the write path before building the PutPipeline. Construction
// (InitStore / OpenStore) uses ValidateComposition, which checks ordering
// without requiring every plugin to be registered yet.
func (r *Runner) ValidateAlgos(algoIDs []string) error {
	for i, algo := range algoIDs {
		factory, err := r.transformers.Get(algo)
		if err != nil {
			return fmt.Errorf("pipeline: %q: %w", algo, err)
		}
		if _, isAEAD := factory.(AEADCapable); isAEAD && i != len(algoIDs)-1 {
			return fmt.Errorf(
				"pipeline: crypto stage %q must be last, found %q after it: %w",
				algo, algoIDs[i+1], errs.ErrInvalidPipeline)
		}
	}
	return nil
}

// ValidateComposition checks only pipeline COMPOSITION (stage ordering),
// not stage presence: a crypto (AEAD) stage must be terminal, so a
// compressor after a crypto plugin is errs.ErrInvalidPipeline. Algorithms
// missing from the registry are skipped — presence is validated separately
// (ValidateAlgos, on the write path), so an unregistered algorithm surfaces
// at Put as errs.ErrUnsupportedAlgorithm rather than at InitStore/OpenStore.
//
// This is the check run at construction time (InitStore / OpenStore): it
// rejects an illegal composition early without coupling store open to the
// set of registered plugins.
func (r *Runner) ValidateComposition(algoIDs []string) error {
	for i, algo := range algoIDs {
		factory, err := r.transformers.Get(algo)
		if err != nil {
			continue // presence is a write-path concern; cannot classify an unknown stage
		}
		if _, isAEAD := factory.(AEADCapable); isAEAD && i != len(algoIDs)-1 {
			return fmt.Errorf(
				"pipeline: crypto stage %q must be last, found %q after it: %w",
				algo, algoIDs[i+1], errs.ErrInvalidPipeline)
		}
	}
	return nil
}

// decoderReadCloser composes a Reader produced by a Decoder chain
// with the Closer of the underlying source. Closing it closes the
// underlying source; the intermediate readers are streaming io.Pipe
// wrappers and exit cleanly when the underlying source reports EOF or
// error.
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

// countingReader wraps an io.Reader and tracks the number of bytes
// passed through, feeding ContentBytesRead (the original payload
// size). Kept private to the package; store has its own counter for
// the post-pipeline staging stream, a separate concern.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

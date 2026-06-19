package store

import (
	"errors"

	"scrinium.dev/engine/pipeline"
)

// The store↔pipeline glue. The transform engine (Encoder/Decoder chain,
// three-hash teeing, inverse read chain) lives in pipeline.Runner; what
// stays here is store policy plus the accessor that binds a Runner to
// this store's registries.

// pipelineRunner returns a Runner bound to this store's hash and
// transformer registries. A Runner is a cheap wrapper, built per
// operation rather than held as a field, so it stays out of the
// &store{} construction sites. s.hashes / s.transformers remain on the
// store: VerifyOnRead consults s.transformers directly, and manifest /
// system.config hashing use s.hashes.
func (s *store) pipelineRunner() *pipeline.Runner {
	return pipeline.NewRunner(s.hashes, s.transformers)
}

// errPipelineWithInline is returned when an Inline blob would have to
// flow through a non-empty Pipeline — not yet supported. Store policy,
// so it lives here rather than in the engine.
var errPipelineWithInline = errors.New(
	"store.Put: Pipeline transforms on Inline blobs are not supported in M2.1")

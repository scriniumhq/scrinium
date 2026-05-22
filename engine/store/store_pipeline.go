package store

import (
	"errors"

	"scrinium.dev/engine/pipeline"
)

// store_pipeline.go — the store↔pipeline glue. The transform engine
// itself (the Encoder/Decoder chain, the three-hash teeing, the
// inverse read chain) now lives in pipeline.Runner; see
// engine/pipeline/runner.go. What stays here is store policy and the
// thin accessor that binds a Runner to this store's registries.

// pipelineRunner returns a pipeline.Runner bound to this store's hash
// and transformer registries. A Runner is a cheap struct wrapper, so
// it is built per operation rather than held as a field — that keeps
// it out of the ~9 &store{} construction sites. s.hashes and
// s.transformers remain on the store: they are also used outside the
// runner (VerifyOnRead consults s.transformers directly; manifest
// ArtifactID and system.config hashing use s.hashes).
func (s *store) pipelineRunner() *pipeline.Runner {
	return pipeline.NewRunner(s.hashes, s.transformers)
}

// errPipelineWithInline is returned when an Inline blob would have to
// flow through a non-empty Pipeline. Inline + Pipeline is reserved
// for a later milestone (see backlog "M2-extra: Pipeline on inline
// blobs"). This is store policy, so it stays here rather than moving
// into the engine.
var errPipelineWithInline = errors.New(
	"store.Put: Pipeline transforms on Inline blobs are not supported in M2.1")

// Package chunker implements the decorator that transparently
// CDC-slices large streams into anonymous chunks and creates a TOC
// manifest.
//
// The slicing algorithm — FastCDC — is fixed in the format
// (ADR-44): the algorithm identifier is NOT stored in the TOC blob,
// which guarantees a deterministic read path and Recovery without
// knowing the configuration.
//
// Slicing is transparent to the client: Put returns a regular
// ArtifactID (the TOC manifest), Get returns the reassembled stream,
// and Walk presents TOC manifests like ordinary artifacts.
//
// Moved from engine/curator/chunker per ADR-53: wrappers are a
// peer concept to Curator (a multi-store orchestrator) and live
// under engine/wrapper/.
//
// TODO(M5.2): CDC-based chunker wrapper.
package chunker

import (
	"fmt"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/wrapper/multistore"
)

// ChunkerConfig holds the slicing parameters. The algorithm
// (FastCDC) is hard-wired; only sizes and the window are tunable.
type ChunkerConfig struct {
	// MinChunkSize, AvgChunkSize, MaxChunkSize define the chunk-size
	// distribution. FastCDC produces a distribution peaking near
	// AvgChunkSize with tails clipped by MinChunkSize/MaxChunkSize.
	MinChunkSize int64
	AvgChunkSize int64
	MaxChunkSize int64

	// HashWindow is the size of the rolling-hash sliding window in
	// bytes.
	HashWindow int
}

// New returns a WrapperFactory for registration in Curator through
// WithStore. Using chunker.Wrapper on a Backup is forbidden (the
// Rules Engine rejects the configuration) — slicing on a Backup
// produces a different ArtifactID and breaks cross-store
// deduplication.
//
// The returned Wrap is a plain core.DataStore without an extension:
// the chunker does not need an explicit Flush, every Put is
// self-contained.
//
// TODO(M5.2): CDC-based chunker wrapper.
func New(cfg ChunkerConfig) multistore.WrapperFactory {
	return &factory{cfg: cfg}
}

type factory struct {
	cfg ChunkerConfig
}

func (f *factory) Wrap(store core.DataStore, deps multistore.WrapperDeps) (core.DataStore, error) {
	return nil, fmt.Errorf("%w: chunker.Wrap", errs.ErrNotImplemented)
}

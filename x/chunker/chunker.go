// Package chunker implements the decorator that transparently
// CDC-slices large streams into anonymous chunks and records them as a
// composite: an ordinary manifest whose blob_refs carry the ordered
// chunk list, with the composite flag in the ext pocket (ADR-87). There
// is no TOC blob and no manifest type field.
//
// The slicing algorithm — FastCDC — is fixed in the format
// (ADR-44): the algorithm identifier is NOT stored anywhere in the
// composite, which guarantees a deterministic read path and Recovery
// without knowing the configuration.
//
// Slicing is transparent to the client: Put returns a regular
// ArtifactID (the floating handle of the composite manifest), Get
// returns the reassembled stream, and Walk presents composites like
// ordinary artifacts.
//
// TODO(M5): CDC-based chunker wrapper (milestones C3).
package chunker

import (
	"fmt"

	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
)

// Config holds the slicing parameters. The algorithm
// (FastCDC) is hard-wired; only sizes and the window are tunable.
//
// TODO(M4.5, decision R4): this struct is fed from the immutable
// StoreConfig.ChunkerConfig field (Types / 11 Configuration §11.3;
// ADR-44) — the field does not exist in domain.StoreConfig yet and
// lands with the chunker milestone. Immutable because changing the
// slicing parameters breaks chunk dedup between old and new writes.
type Config struct {
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

// New returns a WrapperFactory for registration as a Target decorator through
// WithStore. Using chunker.Wrapper on a Backup is forbidden (the
// Rules Engine rejects the configuration) — slicing on a Backup
// produces a different ArtifactID and breaks cross-store
// deduplication.
//
// The returned Wrap is a plain store.DataStore without a custom index:
// the chunker does not need an explicit Flush, every Put is
// self-contained.
//
// TODO(M5): CDC-based chunker wrapper (milestones C3).
func New(cfg Config) wrapper.Factory {
	return &factory{cfg: cfg}
}

type factory struct {
	cfg Config
}

func (f *factory) Wrap(store store.DataStore, deps wrapper.Deps) (store.DataStore, error) {
	return nil, fmt.Errorf("%w: chunker.Wrap", errs.ErrNotImplemented)
}

// Descriptor reports the chunker's identity for the wrapper registry and
// the Rules Engine. chunker is Structural — part of the closed set
// {chunker, bundler}.
func (f *factory) Descriptor() wrapper.Descriptor {
	return wrapper.Descriptor{Name: "chunker", Class: wrapper.Structural}
}

// init registers the chunker under its name for blank-import wiring
// (ADR-63), the way drivers and agents register, so hosts can discover
// the wrapper by name. Construction-time config is applied via New; this
// registers the default factory. (chunker.Wrap itself lands in M4.5.)
func init() { wrapper.Register(New(Config{})) }

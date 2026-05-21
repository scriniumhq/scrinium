package zstd

import (
	"io"

	"github.com/klauspost/compress/zstd"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
)

// Options configures the factory. Zero values pick spec defaults.
type Options struct {
	// EntropyWindowSize is the prefix size, in bytes, on which the
	// Encoder samples Shannon entropy to decide whether the input
	// looks already-compressed. Default: 8192 (8 KiB).
	EntropyWindowSize int

	// EntropyThreshold is the upper entropy bound, in bits per byte
	// (0..8), at which the Encoder switches to its fastest level.
	// Default: 7.7.
	EntropyThreshold float64

	// CompressorBypassLimit is the input-size threshold, in bytes,
	// below which compression is unprofitable (frame overhead
	// dominates) and the Encoder falls back to its fastest level.
	// Default: 512.
	CompressorBypassLimit int

	// Level is the default zstd level for streams that are not
	// bypassed. Default: zstd.SpeedDefault.
	Level zstd.EncoderLevel
}

const (
	defaultEntropyWindowSize     = 8 * 1024
	defaultEntropyThreshold      = 7.7
	defaultCompressorBypassLimit = 512
)

func (o Options) withDefaults() Options {
	if o.EntropyWindowSize <= 0 {
		o.EntropyWindowSize = defaultEntropyWindowSize
	}
	if o.EntropyThreshold <= 0 {
		o.EntropyThreshold = defaultEntropyThreshold
	}
	if o.CompressorBypassLimit < 0 {
		o.CompressorBypassLimit = defaultCompressorBypassLimit
	}
	if o.CompressorBypassLimit == 0 {
		o.CompressorBypassLimit = defaultCompressorBypassLimit
	}
	if o.Level == 0 {
		o.Level = zstd.SpeedDefault
	}
	return o
}

// factory is the zstd TransformerFactory.
type factory struct {
	opts Options
}

// New constructs a zstd TransformerFactory with the given options.
// Zero-valued fields are filled with the spec defaults.
func New(opts Options) core.TransformerFactory {
	return &factory{opts: opts.withDefaults()}
}

// NewEncoder creates a fresh per-operation Encoder. zstd is keyless
// and ignores ec.
func (f *factory) NewEncoder(_ core.EncodeContext) core.Encoder {
	return newEncoder(f.opts)
}

// NewDecoder creates a fresh per-operation Decoder. stage.IV is
// unused by zstd; stage.Algorithm and stage.Hash are informational
// only — the runner has already validated them.
func (f *factory) NewDecoder(stage domain.PipelineStage) core.Decoder {
	_ = stage // zstd is stateless across Put/Get
	return &decoder{}
}

// io.Reader sentinel — keeps the import non-empty when the file
// stands alone. The real reader work happens in encoder.go /
// decoder.go.
var _ io.Reader = (*nopReader)(nil)

type nopReader struct{}

func (nopReader) Read([]byte) (int, error) { return 0, io.EOF }

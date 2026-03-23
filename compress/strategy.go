package compress

import (
	"github.com/klauspost/compress/zstd"
)

// Strategy defines Zstd compression parameters based on payload heuristics.
type Strategy struct {
	ShouldCompress bool
	Level          zstd.EncoderLevel
	ChunkSize      int // Raw data size per chunk
}

var Uncompressed = Strategy{ShouldCompress: false}

// GetStrategy analyzes file metrics to determine the best Zstd approach.
func GetStrategy(size int64, entropy float64, isArtifact bool) Strategy {
	// 1. Skip already compressed/encrypted data (High Entropy)
	// Typical threshold for compressed files is ~7.5-7.8
	if entropy > 7.7 {
		return Strategy{ShouldCompress: false}
	}

	// 2. Skip tiny files (Header overhead > gain)
	if size < 4096 {
		return Strategy{ShouldCompress: false}
	}

	// Default settings for sources
	res := Strategy{
		ShouldCompress: true,
		Level:          zstd.SpeedDefault,
		ChunkSize:      1024 * 1024, // 1MB chunks for good Seek/Ratio balance
	}

	if isArtifact {
		// Artifacts (covers/previews) are read often: use fastest decompression
		res.Level = zstd.SpeedFastest
		res.ChunkSize = 256 * 1024 // Smaller chunks for fast UI range-requests
	} else if entropy < 4.0 && size > 10*1024*1024 {
		// Very low entropy (raw text, logs) and large file: use better compression
		res.Level = zstd.SpeedBetterCompression
	}

	return res
}

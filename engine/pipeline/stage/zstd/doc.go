// Package zstd provides a Scrinium TransformerFactory for the zstd
// compression algorithm via github.com/klauspost/compress/zstd.
//
// Wiring (typical host setup):
//
//	reg := pipeline.NewTransformerRegistry().
//	    Register("zstd", zstd.New(zstd.Options{}))
//	store, _, _ := store.InitStore(ctx, drv,
//	    store.WithReadRegistry(reg), /* ... */)
//
// Stream contract.
// The factory yields fresh per-operation Encoder and Decoder
// instances. Both run in O(1) memory: the Encoder wraps the input
// reader, returning a reader that produces a valid zstd frame; the
// Decoder wraps a zstd frame and exposes the original bytes. No
// part of the stream is buffered in full — data is pulled lazily by
// the consumer.
//
// Bypass heuristics (per docs §7.1).
// Streams that look already-compressed (high Shannon entropy on a
// leading window) or are too small to amortise zstd-frame overhead
// are encoded at the fastest level — the resulting stage in the
// manifest looks ordinary and the decode path is unchanged. The
// thresholds (window size, entropy ceiling, microsize bypass) live
// on Options and are spec-defaulted; see Options for guidance.
package zstd

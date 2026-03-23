package compress

import (
	"io"

	"github.com/klauspost/compress/zstd"
)

// ChunkedWriter implements frame-based compression for Zstd.
type ChunkedWriter struct {
	counter   *metricsCounter
	encoder   *zstd.Encoder
	chunkSize int
	processed int
	offsets   []int64
	level     zstd.EncoderLevel
}

func NewChunkedWriter(dest io.Writer, s Strategy) (*ChunkedWriter, error) {
	cw := &metricsCounter{w: dest}
	enc, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(s.Level))
	if err != nil {
		return nil, err
	}

	return &ChunkedWriter{
		counter:   cw,
		encoder:   enc,
		chunkSize: s.ChunkSize,
		offsets:   []int64{0}, // First frame starts at offset 0
		level:     s.Level,
	}, nil
}

func (w *ChunkedWriter) Write(p []byte) (n int, err error) {
	total := len(p)
	for len(p) > 0 {
		remaining := w.chunkSize - w.processed
		toWrite := len(p)
		if toWrite > remaining {
			toWrite = remaining
		}

		nw, err := w.encoder.Write(p[:toWrite])
		if err != nil {
			return n, err
		}

		n += nw
		w.processed += nw
		p = p[nw:]

		// If current chunk is full, close frame and start a new one.
		if w.processed >= w.chunkSize && len(p) > 0 {
			if err := w.encoder.Close(); err != nil {
				return n, err
			}
			w.processed = 0
			// Record the physical offset where the NEXT frame header begins.
			w.offsets = append(w.offsets, w.counter.count)

			newEnc, _ := zstd.NewWriter(w.counter, zstd.WithEncoderLevel(w.level))
			w.encoder = newEnc
		}
	}
	return total, nil
}

func (w *ChunkedWriter) Offsets() []int64   { return w.offsets }
func (w *ChunkedWriter) EncodedSize() int64 { return w.counter.count }

func (w *ChunkedWriter) Close() error {
	return w.encoder.Close()
}

type metricsCounter struct {
	w     io.Writer
	count int64
}

func (c *metricsCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.count += int64(n)
	return n, err
}

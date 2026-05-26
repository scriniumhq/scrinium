package zstd

import (
	"errors"
	"io"

	"github.com/klauspost/compress/zstd"
)

// decoder is the per-operation Decoder for zstd.
type decoder struct{}

// Transform wraps the input reader, exposing the original
// uncompressed bytes. The zstd reader handles both regular and
// bypass frames (literal-only blocks emitted by the Encoder under
// SpeedFastest) without distinction.
func (d *decoder) Transform(r io.Reader) io.Reader {
	zr, err := zstd.NewReader(r)
	if err != nil {
		// Surface the construction error on the first Read; this
		// keeps the io.Reader contract clean.
		pr, pw := io.Pipe()
		_ = pw.CloseWithError(err)
		return pr
	}

	pr, pw := io.Pipe()
	go func() {
		defer zr.Close()
		_, copyErr := io.Copy(pw, zr)
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			_ = pw.CloseWithError(copyErr)
			return
		}
		_ = pw.Close()
	}()
	return pr
}

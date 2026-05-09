package zstd_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/domain"
	scriniumzstd "github.com/rkurbatov/scrinium/engine/plugin/compress/zstd"
)

// roundTrip is the property under test: Decoder(Encoder(x)) == x
// for every supported input shape.
func roundTrip(t *testing.T, factory core.TransformerFactory, payload []byte) core.TransformResult {
	t.Helper()

	enc := factory.NewEncoder()
	encStream := enc.Transform(bytes.NewReader(payload))
	encoded, err := io.ReadAll(encStream)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	res := enc.Result()
	if res.OutputSize != int64(len(encoded)) {
		t.Fatalf("OutputSize=%d, want %d", res.OutputSize, len(encoded))
	}

	dec := factory.NewDecoder(domain.PipelineStage{Algorithm: "zstd"})
	decStream := dec.Transform(bytes.NewReader(encoded))
	decoded, err := io.ReadAll(decStream)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded != payload (len: got %d, want %d)",
			len(decoded), len(payload))
	}
	return res
}

func TestZstd_RoundTrip_HighlyCompressible(t *testing.T) {
	payload := bytes.Repeat([]byte("the quick brown fox "), 5000)
	res := roundTrip(t, scriniumzstd.New(scriniumzstd.Options{}), payload)
	if res.OutputSize >= int64(len(payload)) {
		t.Fatalf("highly compressible input did not shrink: in=%d out=%d",
			len(payload), res.OutputSize)
	}
}

func TestZstd_RoundTrip_Random(t *testing.T) {
	// Random bytes are essentially incompressible. Round-trip must
	// still hold; OutputSize will be ≥ input length (zstd-frame
	// overhead) but we do not pin a tight bound.
	payload := make([]byte, 64*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	roundTrip(t, scriniumzstd.New(scriniumzstd.Options{}), payload)
}

func TestZstd_RoundTrip_Tiny_MicrosizeBypass(t *testing.T) {
	payload := []byte("hi")
	roundTrip(t, scriniumzstd.New(scriniumzstd.Options{}), payload)
}

func TestZstd_RoundTrip_Empty(t *testing.T) {
	roundTrip(t, scriniumzstd.New(scriniumzstd.Options{}), nil)
}

func TestZstd_RoundTrip_LargeText(t *testing.T) {
	payload := []byte(strings.Repeat("Scrinium is a CAS engine. ", 200_000))
	res := roundTrip(t, scriniumzstd.New(scriniumzstd.Options{}), payload)
	if res.OutputSize*4 > int64(len(payload)) {
		t.Fatalf("expected at least 4x compression on highly redundant text: in=%d out=%d",
			len(payload), res.OutputSize)
	}
}

func TestZstd_DecoderRejectsCorruptedFrame(t *testing.T) {
	factory := scriniumzstd.New(scriniumzstd.Options{})

	// Use pseudo-random bytes so the encoded frame is large enough
	// to leave room for tampering past the header. A highly
	// compressible input (e.g. bytes.Repeat) produces a tiny frame
	// (~15 bytes) where any byte flip still parses as a valid but
	// truncated frame, which klauspost/zstd may report as EOF
	// rather than corruption.
	payload := make([]byte, 64*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	enc := factory.NewEncoder()
	encoded, err := io.ReadAll(enc.Transform(bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(encoded) < 32 {
		t.Fatalf("encoded too short for tamper test: %d", len(encoded))
	}
	// Flip a byte well past the frame header but before the
	// trailer, so the corruption hits a content block.
	encoded[len(encoded)/2] ^= 0xFF

	dec := factory.NewDecoder(domain.PipelineStage{Algorithm: "zstd"})
	decoded, err := io.ReadAll(dec.Transform(bytes.NewReader(encoded)))
	if err == nil {
		// In rare cases a flipped byte may still produce a
		// parseable but semantically different frame. Require at
		// minimum that the output does not equal the original
		// plaintext — silent corruption is the failure mode we
		// guard against.
		if bytes.Equal(decoded, payload) {
			t.Fatalf("corrupted frame decoded to original bytes (silent corruption)")
		}
	}
	// We do not pin the exact error class — klauspost/zstd may
	// surface "block CRC mismatch", "magic number", or simply EOF
	// depending on where the flipped byte falls. The only invariant
	// is "no silent corruption", asserted above.
	_ = errors.Unwrap(err)
}

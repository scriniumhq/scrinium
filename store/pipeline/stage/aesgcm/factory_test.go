package aesgcm_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/store/pipeline"
	"scrinium.dev/store/pipeline/stage/aesgcm"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func encode(t *testing.T, f pipeline.TransformerFactory, ec pipeline.EncodeContext, plain []byte) ([]byte, pipeline.TransformResult) {
	t.Helper()
	enc := f.NewEncoder(ec)
	ct, err := io.ReadAll(enc.Transform(bytes.NewReader(plain)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return ct, enc.Result()
}

func TestAESGCM_RoundTrip(t *testing.T) {
	factory, err := aesgcm.New(mustKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload := []byte("Scrinium is a content-addressable store.")

	ct, res := encode(t, factory, pipeline.EncodeContext{}, payload)
	// Segmented format: per-blob IV is gone — Result.IV must be nil.
	if res.IV != nil {
		t.Fatalf("Result.IV must be nil for segmented format, got %d bytes", len(res.IV))
	}
	if res.KeyID != "" {
		t.Fatalf("pinned factory must not record KeyID, got %q", res.KeyID)
	}
	if res.OutputSize != int64(len(ct)) {
		t.Fatalf("OutputSize=%d, want %d", res.OutputSize, len(ct))
	}
	// Header + frame overhead means the blob is larger than payload.
	if len(ct) <= len(payload) {
		t.Fatalf("framed blob (%d) must exceed payload (%d)", len(ct), len(payload))
	}

	dec := factory.NewDecoder(domain.PipelineStage{Algorithm: "aes-gcm"})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Fatalf("decoded != payload")
	}
}

// The decoder takes the IV from each frame, never from stage.IV — a
// bogus stage IV must not affect the result.
func TestAESGCM_DecoderIgnoresStageIV(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))
	ct, _ := encode(t, factory, pipeline.EncodeContext{}, []byte("ignore the stage IV"))

	bogus := bytes.Repeat([]byte{0xAB}, 12)
	dec := factory.NewDecoder(domain.PipelineStage{Algorithm: "aes-gcm", IV: bogus})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(pt) != "ignore the stage IV" {
		t.Fatalf("got %q", pt)
	}
}

func TestAESGCM_WrongKeyFailsAEAD(t *testing.T) {
	f1, _ := aesgcm.New(mustKey(t))
	f2, _ := aesgcm.New(mustKey(t))

	ct, _ := encode(t, f1, pipeline.EncodeContext{}, []byte("secret"))
	dec := f2.NewDecoder(domain.PipelineStage{})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

func TestAESGCM_TamperedCiphertextFailsAEAD(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))
	ct, _ := encode(t, factory, pipeline.EncodeContext{}, bytes.Repeat([]byte{'x'}, 1024))

	// Flip a byte near the end (inside the last segment's tag region).
	ct[len(ct)-1] ^= 0x01
	dec := factory.NewDecoder(domain.PipelineStage{})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

func TestAESGCM_BadKeyLengthAtConstruction(t *testing.T) {
	for _, n := range []int{0, 16, 24, 31, 33, 64} {
		_, err := aesgcm.New(make([]byte, n))
		if !errors.Is(err, aesgcm.ErrInvalidKeyLength) {
			t.Fatalf("len=%d: got %v, want ErrInvalidKeyLength", n, err)
		}
	}
}

func TestAESGCM_FactoryReusableAcrossOperations(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))
	for i := 0; i < 5; i++ {
		ct, _ := encode(t, factory, pipeline.EncodeContext{},
			[]byte("payload "+string(rune('a'+i))))
		dec := factory.NewDecoder(domain.PipelineStage{})
		if _, err := io.ReadAll(dec.Transform(bytes.NewReader(ct))); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}

// Convergent: same key + same plaintext → identical ciphertext bytes.
// Disabled (default): same input → different bytes (random IVs).
func TestAESGCM_ConvergentDeterministicVsDisabled(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))
	payload := bytes.Repeat([]byte{'z'}, 4096)

	convCtx := pipeline.EncodeContext{EncryptedDedup: domain.EncryptedDedupConvergent, SegmentSize: 1024}
	a, _ := encode(t, factory, convCtx, payload)
	b, _ := encode(t, factory, convCtx, payload)
	if !bytes.Equal(a, b) {
		t.Fatal("Convergent: identical input must produce identical bytes")
	}
	// And it still decrypts.
	dec := factory.NewDecoder(domain.PipelineStage{})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(a)))
	if err != nil || !bytes.Equal(pt, payload) {
		t.Fatalf("convergent round-trip: err=%v eq=%v", err, bytes.Equal(pt, payload))
	}

	disCtx := pipeline.EncodeContext{EncryptedDedup: domain.EncryptedDedupDisabled, SegmentSize: 1024}
	c, _ := encode(t, factory, disCtx, payload)
	d, _ := encode(t, factory, disCtx, payload)
	if bytes.Equal(c, d) {
		t.Fatal("Disabled: identical input must NOT produce identical bytes")
	}
}

// Multi-segment round-trip with a small SegmentSize forces several
// frames; the streaming encoder must not buffer the whole blob.
func TestAESGCM_MultiSegmentRoundTrip(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))
	payload := make([]byte, 1024*10+57)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	ec := pipeline.EncodeContext{SegmentSize: 1024}
	ct, _ := encode(t, factory, ec, payload)

	dec := factory.NewDecoder(domain.PipelineStage{})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Fatal("multi-segment round-trip mismatch")
	}
}

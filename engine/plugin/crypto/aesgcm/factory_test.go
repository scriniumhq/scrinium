package aesgcm_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/plugin/crypto/aesgcm"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestAESGCM_RoundTrip(t *testing.T) {
	key := mustKey(t)
	factory, err := aesgcm.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	payload := []byte("Scrinium is a content-addressable store.")

	enc := factory.NewEncoder()
	ct, err := io.ReadAll(enc.Transform(bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	res := enc.Result()
	if len(res.IV) != 12 {
		t.Fatalf("Result.IV length = %d, want 12", len(res.IV))
	}
	if res.OutputSize != int64(len(ct)) {
		t.Fatalf("OutputSize=%d, want %d", res.OutputSize, len(ct))
	}
	// AEAD output is plaintext + 16-byte tag.
	if len(ct) != len(payload)+16 {
		t.Fatalf("ciphertext len = %d, want %d", len(ct), len(payload)+16)
	}

	dec := factory.NewDecoder(domain.PipelineStage{
		Algorithm: "aes-gcm",
		IV:        res.IV,
	})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Fatalf("decoded != payload")
	}
}

func TestAESGCM_WrongKeyFailsAEAD(t *testing.T) {
	factory1, _ := aesgcm.New(mustKey(t))
	factory2, _ := aesgcm.New(mustKey(t))

	enc := factory1.NewEncoder()
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader([]byte("secret"))))
	iv := enc.Result().IV

	dec := factory2.NewDecoder(domain.PipelineStage{IV: iv})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err == nil {
		t.Fatalf("expected error decrypting with wrong key, got nil")
	}
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

func TestAESGCM_TamperedCiphertextFailsAEAD(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))

	enc := factory.NewEncoder()
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader(
		bytes.Repeat([]byte{'x'}, 1024))))
	iv := enc.Result().IV

	// Flip a bit in the middle of the ciphertext (well before the
	// tag).
	ct[len(ct)/2] ^= 0x01

	dec := factory.NewDecoder(domain.PipelineStage{IV: iv})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

func TestAESGCM_WrongIVFailsAEAD(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))

	enc := factory.NewEncoder()
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader([]byte("payload"))))
	wrongIV := make([]byte, 12)
	for i := range wrongIV {
		wrongIV[i] = byte(i + 1)
	}

	dec := factory.NewDecoder(domain.PipelineStage{IV: wrongIV})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

func TestAESGCM_BadKeyLengthAtConstruction(t *testing.T) {
	cases := []int{0, 16, 24, 31, 33, 64}
	for _, n := range cases {
		_, err := aesgcm.New(make([]byte, n))
		if !errors.Is(err, aesgcm.ErrInvalidKeyLength) {
			t.Fatalf("len=%d: got %v, want ErrInvalidKeyLength", n, err)
		}
	}
}

func TestAESGCM_FactoryReusableAcrossOperations(t *testing.T) {
	factory, _ := aesgcm.New(mustKey(t))

	for i := 0; i < 5; i++ {
		enc := factory.NewEncoder()
		ct, _ := io.ReadAll(enc.Transform(bytes.NewReader(
			[]byte("payload " + string(rune('a'+i))))))
		iv := enc.Result().IV

		dec := factory.NewDecoder(domain.PipelineStage{IV: iv})
		_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}

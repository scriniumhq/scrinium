package aesgcm_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/plugin/crypto/aesgcm"
)

// fixedKeyResolver returns a single DEK under a single KeyID.
type fixedKeyResolver struct {
	keyID string
	key   []byte
}

func (r *fixedKeyResolver) GetKeys(keyID string) ([][]byte, error) {
	if keyID != r.keyID {
		return nil, nil
	}
	return [][]byte{r.key}, nil
}
func (r *fixedKeyResolver) ResolveWriteKey(pipeline.KeyContext) string { return r.keyID }

// rotatingKeyResolver returns multiple DEK candidates under one KeyID
// — what the engine sees during a RotateKEK window.
type rotatingKeyResolver struct {
	keyID        string
	defaultKey   []byte
	previousKeys [][]byte
}

func (r *rotatingKeyResolver) GetKeys(keyID string) ([][]byte, error) {
	if keyID != r.keyID {
		return nil, nil
	}
	out := make([][]byte, 0, 1+len(r.previousKeys))
	out = append(out, r.defaultKey)
	out = append(out, r.previousKeys...)
	return out, nil
}
func (r *rotatingKeyResolver) ResolveWriteKey(pipeline.KeyContext) string { return r.keyID }

func TestAESGCM_Resolver_RoundTrip(t *testing.T) {
	resolver := &fixedKeyResolver{keyID: "tenant-a", key: mustKey(t)}
	factory := aesgcm.NewWithResolver(resolver)

	payload := []byte("ciphertext flows through resolver")
	ct, res := encode(t, factory, pipeline.EncodeContext{KeyID: "tenant-a"}, payload)
	if res.KeyID != "tenant-a" {
		t.Errorf("Result.KeyID: got %q, want %q", res.KeyID, "tenant-a")
	}
	if res.IV != nil {
		t.Errorf("Result.IV must be nil for segmented format, got %d bytes", len(res.IV))
	}
	if res.OutputSize != int64(len(ct)) {
		t.Errorf("OutputSize=%d, want %d", res.OutputSize, len(ct))
	}

	dec := factory.NewDecoder(domain.PipelineStage{Algorithm: "aes-gcm", KeyID: res.KeyID})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Errorf("roundtrip: got %q, want %q", pt, payload)
	}
}

// Rotation: a blob written under DEK_v1 still decrypts when the
// resolver now returns DEK_v2 first, then DEK_v1.
func TestAESGCM_Resolver_RotationDecryptsOldBlob(t *testing.T) {
	oldKey := mustKey(t)
	newKey := mustKey(t)

	writeFactory := aesgcm.NewWithResolver(&fixedKeyResolver{keyID: "active", key: oldKey})
	payload := []byte("rotation-survivor blob")
	ct, res := encode(t, writeFactory, pipeline.EncodeContext{KeyID: "active"}, payload)

	readResolver := &rotatingKeyResolver{
		keyID:        "active",
		defaultKey:   newKey,
		previousKeys: [][]byte{oldKey},
	}
	readFactory := aesgcm.NewWithResolver(readResolver)
	dec := readFactory.NewDecoder(domain.PipelineStage{KeyID: res.KeyID})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("rotation decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Errorf("rotation roundtrip: got %q, want %q", pt, payload)
	}
}

// Unknown KeyID: the resolver returns no keys; the failure is a
// resolver-side error, NOT ErrDecryptionFailed (which is reserved for
// AEAD tag mismatch).
func TestAESGCM_Resolver_UnknownKeyIDFailsBeforeOpen(t *testing.T) {
	factory := aesgcm.NewWithResolver(&fixedKeyResolver{keyID: "real", key: mustKey(t)})
	ct, _ := encode(t, factory, pipeline.EncodeContext{KeyID: "real"}, []byte("body"))

	dec := factory.NewDecoder(domain.PipelineStage{KeyID: "phantom"})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err == nil {
		t.Fatalf("expected error for unknown KeyID, got nil")
	}
	if errors.Is(err, errs.ErrDecryptionFailed) {
		t.Errorf("unknown KeyID surfaced as ErrDecryptionFailed; want a distinct error, got %v", err)
	}
}

func TestAESGCM_Resolver_TamperedCiphertextFailsAEAD(t *testing.T) {
	factory := aesgcm.NewWithResolver(&fixedKeyResolver{keyID: "k", key: mustKey(t)})
	ct, res := encode(t, factory, pipeline.EncodeContext{KeyID: "k"}, bytes.Repeat([]byte{'x'}, 512))

	ct[len(ct)-1] ^= 0x01
	dec := factory.NewDecoder(domain.PipelineStage{KeyID: res.KeyID})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

func TestAESGCM_Resolver_NilResolverFailsOnTransform(t *testing.T) {
	factory := aesgcm.NewWithResolver(nil)
	enc := factory.NewEncoder(pipeline.EncodeContext{})
	_, err := io.ReadAll(enc.Transform(bytes.NewReader([]byte("anything"))))
	if err == nil {
		t.Fatal("expected error for nil resolver, got nil")
	}
}

func TestAESGCM_Resolver_IsAEADCapable(t *testing.T) {
	factory := aesgcm.NewWithResolver(&fixedKeyResolver{keyID: "x", key: mustKey(t)})
	if _, ok := factory.(pipeline.AEADCapable); !ok {
		t.Fatal("resolver-backed factory must implement plugin.AEADCapable")
	}
}

// Convergent under the resolver: same KeyID+DEK+plaintext →
// byte-identical blob; a different KeyID changes the bytes.
func TestAESGCM_Resolver_ConvergentKeyIDSplitsCiphertext(t *testing.T) {
	dek := mustKey(t)
	// One resolver, two KeyIDs mapping to the same DEK, so only the
	// KeyID differs in the convergent derivation.
	resolver := &twoKeyResolver{a: "ka", b: "kb", key: dek}
	factory := aesgcm.NewWithResolver(resolver)
	payload := bytes.Repeat([]byte{'q'}, 4096)

	conv := func(keyID string) []byte {
		ct, _ := encode(t, factory, pipeline.EncodeContext{
			KeyID: keyID, EncryptedDedup: domain.EncryptedDedupConvergent, SegmentSize: 1024,
		}, payload)
		return ct
	}
	if bytes.Equal(conv("ka"), conv("kb")) {
		t.Fatal("convergent: distinct KeyID must change ciphertext")
	}
	// Same KeyID twice is deterministic.
	if !bytes.Equal(conv("ka"), conv("ka")) {
		t.Fatal("convergent: same KeyID must be deterministic")
	}
}

type twoKeyResolver struct {
	a, b string
	key  []byte
}

func (r *twoKeyResolver) GetKeys(keyID string) ([][]byte, error) {
	if keyID == r.a || keyID == r.b {
		return [][]byte{r.key}, nil
	}
	return nil, nil
}
func (r *twoKeyResolver) ResolveWriteKey(pipeline.KeyContext) string { return r.a }

func TestAESGCM_PinnedDEK_StillWorks(t *testing.T) {
	factory, err := aesgcm.New(mustKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, res := encode(t, factory, pipeline.EncodeContext{}, []byte("pinned"))
	if res.KeyID != "" {
		t.Errorf("pinned factory must NOT record KeyID, got %q", res.KeyID)
	}
	dec := factory.NewDecoder(domain.PipelineStage{})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(pt) != "pinned" {
		t.Errorf("got %q, want pinned", pt)
	}
}

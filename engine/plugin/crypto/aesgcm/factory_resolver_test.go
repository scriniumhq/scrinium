package aesgcm_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/plugin/crypto/aesgcm"
)

// fixedKeyResolver returns a single DEK under a single KeyID.
// Models the most common production wiring: one tenant, one
// active DEK.
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
func (r *fixedKeyResolver) ResolveWriteKey(coreapi.KeyContext) string { return r.keyID }

// rotatingKeyResolver returns multiple DEK candidates under one
// KeyID — what the engine sees during a RotateKEK window: the
// new DEK is default for writes, the previous DEK is still
// returned so old blobs decrypt.
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
func (r *rotatingKeyResolver) ResolveWriteKey(coreapi.KeyContext) string { return r.keyID }

// --- Happy path: encode → decode round-trip ---

func TestAESGCM_Resolver_RoundTrip(t *testing.T) {
	resolver := &fixedKeyResolver{
		keyID: "tenant-a",
		key:   mustKey(t),
	}
	factory := aesgcm.NewWithResolver(resolver)

	payload := []byte("ciphertext flows through resolver")
	enc := factory.NewEncoder(coreapi.EncodeContext{KeyID: "tenant-a"})
	ct, err := io.ReadAll(enc.Transform(bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	res := enc.Result()
	if res.KeyID != "tenant-a" {
		t.Errorf("Result.KeyID: got %q, want %q", res.KeyID, "tenant-a")
	}
	if len(res.IV) != 12 {
		t.Errorf("Result.IV length: got %d, want 12", len(res.IV))
	}
	if res.OutputSize != int64(len(ct)) {
		t.Errorf("OutputSize=%d, want %d", res.OutputSize, len(ct))
	}

	dec := factory.NewDecoder(domain.PipelineStage{
		Algorithm: "aes-gcm",
		IV:        res.IV,
		KeyID:     res.KeyID,
	})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Errorf("roundtrip: got %q, want %q", pt, payload)
	}
}

// --- Rotation: old blob decrypts with previous DEK ---
//
// Models RotateKEK: a blob was written under DEK_v1; the resolver
// now returns DEK_v2 first (the new default) followed by DEK_v1.
// The Decoder must succeed by trying candidates in order.

func TestAESGCM_Resolver_RotationDecryptsOldBlob(t *testing.T) {
	oldKey := mustKey(t)
	newKey := mustKey(t)

	// Write side: only the old key is available.
	writeResolver := &fixedKeyResolver{keyID: "active", key: oldKey}
	writeFactory := aesgcm.NewWithResolver(writeResolver)

	payload := []byte("rotation-survivor blob")
	enc := writeFactory.NewEncoder(coreapi.EncodeContext{KeyID: "active"})
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader(payload)))
	res := enc.Result()

	// Read side: post-rotation. DEK_v2 is default, DEK_v1 is in
	// the candidate list.
	readResolver := &rotatingKeyResolver{
		keyID:        "active",
		defaultKey:   newKey,
		previousKeys: [][]byte{oldKey},
	}
	readFactory := aesgcm.NewWithResolver(readResolver)

	dec := readFactory.NewDecoder(domain.PipelineStage{
		IV:    res.IV,
		KeyID: res.KeyID,
	})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("rotation decode: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Errorf("rotation roundtrip: got %q, want %q", pt, payload)
	}
}

// --- Wrong KeyID surfaces a decryption failure ---
//
// The resolver returns no keys for an unknown KeyID, which the
// decoder reports as errKeyResolverEmpty (unexported). We assert
// the outer io.Pipe error chain wraps it; the public sentinel
// for "couldn't decrypt" remains ErrDecryptionFailed on actual
// tag mismatches. Here the failure mode is "no key at all" —
// distinct enough that we don't conflate it with
// ErrDecryptionFailed.

func TestAESGCM_Resolver_UnknownKeyIDFailsBeforeOpen(t *testing.T) {
	resolver := &fixedKeyResolver{keyID: "real", key: mustKey(t)}
	factory := aesgcm.NewWithResolver(resolver)

	enc := factory.NewEncoder(coreapi.EncodeContext{KeyID: "real"})
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader([]byte("body"))))
	iv := enc.Result().IV

	dec := factory.NewDecoder(domain.PipelineStage{
		IV:    iv,
		KeyID: "phantom",
	})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err == nil {
		t.Fatalf("expected error for unknown KeyID, got nil")
	}
	// The decoder did not even reach AEAD.Open; it must not
	// claim ErrDecryptionFailed.
	if errors.Is(err, errs.ErrDecryptionFailed) {
		t.Errorf("unknown KeyID surfaced as ErrDecryptionFailed; "+
			"want a distinct error, got %v", err)
	}
}

// --- Tampered ciphertext: every candidate fails Open ---

func TestAESGCM_Resolver_TamperedCiphertextFailsAEAD(t *testing.T) {
	resolver := &fixedKeyResolver{keyID: "k", key: mustKey(t)}
	factory := aesgcm.NewWithResolver(resolver)

	enc := factory.NewEncoder(coreapi.EncodeContext{KeyID: "k"})
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader(
		bytes.Repeat([]byte{'x'}, 512))))
	iv := enc.Result().IV

	ct[len(ct)/2] ^= 0x01

	dec := factory.NewDecoder(domain.PipelineStage{
		IV:    iv,
		KeyID: enc.Result().KeyID,
	})
	_, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("got %v, want errs.ErrDecryptionFailed", err)
	}
}

// --- Nil resolver: surfaces on first Transform ---

func TestAESGCM_Resolver_NilResolverFailsOnTransform(t *testing.T) {
	factory := aesgcm.NewWithResolver(nil)
	enc := factory.NewEncoder(coreapi.EncodeContext{})
	_, err := io.ReadAll(enc.Transform(bytes.NewReader([]byte("anything"))))
	if err == nil {
		t.Fatal("expected error for nil resolver, got nil")
	}
}

// --- AEADCapable marker is preserved ---

func TestAESGCM_Resolver_IsAEADCapable(t *testing.T) {
	factory := aesgcm.NewWithResolver(&fixedKeyResolver{
		keyID: "x", key: mustKey(t),
	})
	if _, ok := factory.(coreapi.AEADCapable); !ok {
		t.Fatal("resolver-backed factory must implement core.AEADCapable")
	}
}

// --- Pinned-DEK factory legacy path: still works ---
//
// Cross-check that introducing NewWithResolver did not regress
// the pinned-DEK factory. This duplicates a fragment of the
// pre-existing TestAESGCM_RoundTrip but keeps both paths
// observable from a single test file when this one is in scope.

func TestAESGCM_PinnedDEK_StillWorks(t *testing.T) {
	factory, err := aesgcm.New(mustKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	enc := factory.NewEncoder(coreapi.EncodeContext{})
	ct, _ := io.ReadAll(enc.Transform(bytes.NewReader([]byte("pinned"))))
	res := enc.Result()
	if res.KeyID != "" {
		t.Errorf("pinned factory must NOT record KeyID, got %q", res.KeyID)
	}

	dec := factory.NewDecoder(domain.PipelineStage{IV: res.IV})
	pt, err := io.ReadAll(dec.Transform(bytes.NewReader(ct)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(pt) != "pinned" {
		t.Errorf("got %q, want pinned", pt)
	}
}

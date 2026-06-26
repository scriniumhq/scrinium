package systemstore

import (
	"bytes"
	"errors"
	"testing"

	"scrinium.dev/errs"
)

const (
	storeX = "store-X"
	storeY = "store-Y"
)

func TestEnvelope_RoundTrip(t *testing.T) {
	payload := []byte(`{"k":"v"}`)
	blob, err := wrapEnvelope(storeX, payload)
	if err != nil {
		t.Fatalf("wrapEnvelope: %v", err)
	}
	env, err := openEnvelope(blob, storeX)
	if err != nil {
		t.Fatalf("openEnvelope: %v", err)
	}
	if !bytes.Equal(env.InlinePayload, payload) {
		t.Errorf("InlinePayload = %q, want %q", env.InlinePayload, payload)
	}
	if env.StoreID != storeX {
		t.Errorf("StoreID = %q, want %q", env.StoreID, storeX)
	}
}

// A binary payload (e.g. a checkpoint .db until it moves external) round-trips
// through the base64-encoded inline_payload — the envelope must not require the
// payload to be JSON.
func TestEnvelope_BinaryPayloadSafe(t *testing.T) {
	payload := []byte{0x00, 0xff, 0x10, 0x80, 0x00}
	blob, err := wrapEnvelope(storeX, payload)
	if err != nil {
		t.Fatalf("wrapEnvelope (binary): %v", err)
	}
	env, err := openEnvelope(blob, storeX)
	if err != nil {
		t.Fatalf("openEnvelope (binary): %v", err)
	}
	if !bytes.Equal(env.InlinePayload, payload) {
		t.Errorf("binary payload not preserved: %v", env.InlinePayload)
	}
}

func TestEnvelope_ForeignStoreRejected(t *testing.T) {
	blob, _ := wrapEnvelope(storeY, []byte("x"))
	_, err := openEnvelope(blob, storeX)
	if !errors.Is(err, errs.ErrSystemArtifactForeign) {
		t.Fatalf("openEnvelope foreign = %v, want ErrSystemArtifactForeign", err)
	}
}

func TestEnvelope_MissingStoreIDMalformed(t *testing.T) {
	blob, _ := wrapEnvelope("", []byte("x")) // empty store_id
	_, err := openEnvelope(blob, storeX)
	if !errors.Is(err, errs.ErrSystemArtifactMalformed) {
		t.Fatalf("openEnvelope no-store-id = %v, want ErrSystemArtifactMalformed", err)
	}
}

func TestEnvelope_NonJSONMalformed(t *testing.T) {
	_, err := openEnvelope([]byte("}{ not json"), storeX)
	if !errors.Is(err, errs.ErrSystemArtifactMalformed) {
		t.Fatalf("openEnvelope non-json = %v, want ErrSystemArtifactMalformed", err)
	}
}

// An empty authoritative store_id (e.g. a check before the descriptor is read)
// classifies as Unknown, never Foreign, so the read passes — the envelope is
// returned and the consumer proceeds rather than rejecting what it cannot
// verify.
func TestEnvelope_UnverifiableWhenAuthoritativeEmpty(t *testing.T) {
	blob, _ := wrapEnvelope(storeY, []byte("x"))
	env, err := openEnvelope(blob, "")
	if err != nil {
		t.Fatalf("openEnvelope (empty authoritative) = %v, want nil", err)
	}
	if env.StoreID != storeY {
		t.Errorf("StoreID = %q, want %q", env.StoreID, storeY)
	}
}

// A status artifact carries no payload — presence is the signal. wrap/open of
// an empty payload must round-trip to an empty inline_payload, not an error.
func TestEnvelope_StatusArtifactEmptyPayload(t *testing.T) {
	blob, err := wrapEnvelope(storeX, nil)
	if err != nil {
		t.Fatalf("wrapEnvelope (status): %v", err)
	}
	env, err := openEnvelope(blob, storeX)
	if err != nil {
		t.Fatalf("openEnvelope (status): %v", err)
	}
	if len(env.InlinePayload) != 0 {
		t.Errorf("status InlinePayload = %q, want empty", env.InlinePayload)
	}
}

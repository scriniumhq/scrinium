package plugins

import (
	"bytes"
	"testing"
)

// TestStaticKeyResolver_Close_WipesDEK verifies that Close zeroes and
// drops the internal DEK copy, and that GetKeys returns (nil, nil)
// afterwards (the codec maps that to ErrKeyNotFound). This is the
// unit-level counterpart to the store.Close behavioural test, which
// only checks the post-close GetKeys contract through the public
// KeyResolver interface.
func TestStaticKeyResolver_Close_WipesDEK(t *testing.T) {
	dek := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	r := NewStaticKeyResolver(dek).(*staticKeyResolver)

	// Before close: GetKeys returns a defensive copy of the DEK.
	keys, err := r.GetKeys("any")
	if err != nil {
		t.Fatalf("GetKeys before close: %v", err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], dek) {
		t.Fatalf("GetKeys before close: want one copy of dek, got %v", keys)
	}

	r.Close()

	if r.dek != nil {
		t.Errorf("dek after Close: want nil, got %v", r.dek)
	}
	keys, err = r.GetKeys("any")
	if err != nil {
		t.Errorf("GetKeys after close: unexpected err: %v", err)
	}
	if keys != nil {
		t.Errorf("GetKeys after close: want nil, got %v", keys)
	}
}

// TestStaticKeyResolver_Close_Idempotent verifies Close can be called
// repeatedly without panicking.
func TestStaticKeyResolver_Close_Idempotent(t *testing.T) {
	r := NewStaticKeyResolver([]byte{1, 2, 3}).(*staticKeyResolver)
	r.Close()
	r.Close() // must not panic
}

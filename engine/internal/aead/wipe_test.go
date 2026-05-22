package aead_test

import (
	"testing"

	"scrinium.dev/engine/internal/aead"
)

func TestWipe(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	aead.Wipe(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte %d: got %d, want 0", i, v)
		}
	}

	// Nil and empty are no-op safe.
	aead.Wipe(nil)
	aead.Wipe([]byte{})
}

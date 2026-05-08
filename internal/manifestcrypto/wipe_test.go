package manifestcrypto_test

import (
	"testing"

	"github.com/rkurbatov/scrinium/internal/manifestcrypto"
)

func TestWipe(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	manifestcrypto.Wipe(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte %d: got %d, want 0", i, v)
		}
	}

	// Nil and empty are no-op safe.
	manifestcrypto.Wipe(nil)
	manifestcrypto.Wipe([]byte{})
}

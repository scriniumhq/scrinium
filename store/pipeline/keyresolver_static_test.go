package pipeline

import (
	"io"
	"testing"
)

// --- KeyResolver tests ---

func TestStaticKeyResolver_ResolveWriteKey(t *testing.T) {
	r := NewStaticKeyResolver([]byte("dek"))
	// Non-empty namespace asserts the static resolver ignores ctx.
	if got := r.ResolveWriteKey(KeyContext{Namespace: "ns"}); got != "" {
		t.Errorf("ResolveWriteKey: got %q, want empty string", got)
	}
}

func TestStaticKeyResolver_GetKeysReturnsCopy(t *testing.T) {
	original := []byte("super-secret-key")
	r := NewStaticKeyResolver(original)

	keys, err := r.GetKeys("any-key-id")
	if err != nil {
		t.Fatalf("GetKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if string(keys[0]) != string(original) {
		t.Errorf("returned key differs from the original")
	}

	// Mutating the returned slice must not affect the resolver's
	// internal state.
	keys[0][0] = 'X'

	keys2, _ := r.GetKeys("any-key-id")
	if string(keys2[0]) != string(original) {
		t.Errorf("internal state corrupted: %s != %s", keys2[0], original)
	}
}

func TestStaticKeyResolver_InputCopy(t *testing.T) {
	original := []byte("super-secret-key")
	r := NewStaticKeyResolver(original)

	// Mutating the input slice must not affect the internal copy
	// either.
	original[0] = 'X'

	keys, _ := r.GetKeys("any-key-id")
	if keys[0][0] == 'X' {
		t.Errorf("resolver did not copy input on construction")
	}
}

// Smoke test for the lifecycle stubs got removed in M1.3 pack 3:
// both InitStore and OpenStore are real now. Behaviour is covered
// by core_test integration tests in lfiecycle_init_test.go.

// Compile-time sanity: io is imported and used.
var _ io.Reader = (*nopReader)(nil)

type nopReader struct{}

func (nopReader) Read(p []byte) (int, error) { return 0, io.EOF }

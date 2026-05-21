package store

import (
	"crypto/sha256"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"testing"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// --- HashRegistry tests ---

func TestHashRegistry_RegisterAndUse(t *testing.T) {
	r := NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })

	h, err := r.NewHasher("sha256")
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	h.Write([]byte("hello"))
	got := r.Format("sha256", h.Sum(nil))
	want := "sha256-2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Fatalf("Format: got %q, want %q", got, want)
	}
}

func TestHashRegistry_UnsupportedAlgorithm(t *testing.T) {
	r := NewHashRegistry()
	_, err := r.NewHasher("md5")
	if !errors.Is(err, errs.ErrUnsupportedAlgorithm) {
		t.Fatalf("expected errs.ErrUnsupportedAlgorithm, got %v", err)
	}
}

func TestHashRegistry_Parse(t *testing.T) {
	r := NewHashRegistry()
	algo, raw, err := r.Parse("sha256-aabbccdd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if algo != "sha256" {
		t.Errorf("algo: got %q, want %q", algo, "sha256")
	}
	if len(raw) != 4 || raw[0] != 0xaa {
		t.Errorf("raw: got %v, want [0xaa 0xbb 0xcc 0xdd]", raw)
	}
}

func TestHashRegistry_Parse_InvalidFormats(t *testing.T) {
	r := NewHashRegistry()
	cases := []string{
		"",            // empty string
		"sha256",      // no dash
		"-abc",        // empty algo
		"sha256-",     // empty hex
		"sha256-zzzz", // invalid hex
	}
	for _, c := range cases {
		if _, _, err := r.Parse(c); err == nil {
			t.Errorf("Parse(%q): expected error, got nil", c)
		}
	}
}

func TestHashRegistry_RoundTrip(t *testing.T) {
	r := NewHashRegistry().
		Register("crc32", func() hash.Hash { return crc32.NewIEEE() })

	h, _ := r.NewHasher("crc32")
	h.Write([]byte("data"))
	formatted := r.Format("crc32", h.Sum(nil))

	algo, raw, err := r.Parse(formatted)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if algo != "crc32" {
		t.Errorf("algo round-trip: got %q", algo)
	}
	// Same raw form after Parse.
	again := r.Format(algo, raw)
	if again != formatted {
		t.Errorf("round-trip mismatch: %q -> %q", formatted, again)
	}
}

// --- TransformerRegistry tests ---

type stubFactory struct{ id string }

func (f *stubFactory) NewEncoder(ctx coreapi.EncodeContext) coreapi.Encoder { return nil }
func (f *stubFactory) NewDecoder(_ domain.PipelineStage) coreapi.Decoder    { return nil }

func TestTransformerRegistry_RegisterAndGet(t *testing.T) {
	r := NewTransformerRegistry()
	r.Register("zstd", &stubFactory{id: "zstd"})

	f, err := r.Get("zstd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.(*stubFactory).id != "zstd" {
		t.Errorf("wrong factory returned")
	}
}

func TestTransformerRegistry_UnsupportedAlgorithm(t *testing.T) {
	r := NewTransformerRegistry()
	_, err := r.Get("nonexistent")
	if !errors.Is(err, errs.ErrUnsupportedAlgorithm) {
		t.Fatalf("expected errs.ErrUnsupportedAlgorithm, got %v", err)
	}
}

func TestTransformerRegistry_ChainedRegistration(t *testing.T) {
	r := NewTransformerRegistry().
		Register("a", &stubFactory{id: "a"}).
		Register("b", &stubFactory{id: "b"}).
		Register("c", &stubFactory{id: "c"})

	for _, id := range []string{"a", "b", "c"} {
		f, err := r.Get(id)
		if err != nil {
			t.Errorf("Get(%q): %v", id, err)
			continue
		}
		if f.(*stubFactory).id != id {
			t.Errorf("Get(%q) returned wrong factory", id)
		}
	}
}

// --- KeyResolver tests ---

func TestStaticKeyResolver_ResolveWriteKey(t *testing.T) {
	r := NewStaticKeyResolver([]byte("dek"))
	// Non-empty namespace asserts the static resolver ignores ctx.
	if got := r.ResolveWriteKey(coreapi.KeyContext{Namespace: "ns"}); got != "" {
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
// by core_test integration tests in init_test.go.

// Compile-time sanity: io is imported and used.
var _ io.Reader = (*nopReader)(nil)

type nopReader struct{}

func (nopReader) Read(p []byte) (int, error) { return 0, io.EOF }

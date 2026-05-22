package hashing

import (
	"crypto/sha256"
	"errors"
	"hash"
	"hash/crc32"
	"testing"

	"scrinium.dev/engine/errs"
)

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
	again := r.Format(algo, raw)
	if again != formatted {
		t.Errorf("round-trip mismatch: %q -> %q", formatted, again)
	}
}

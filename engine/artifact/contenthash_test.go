package artifact_test

import (
	"crypto/sha256"
	"hash"
	"testing"

	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/hashing"
)

func testRegistry() domain.HashRegistry {
	return hashing.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

func TestParseContentHash_ValidRoundTrip(t *testing.T) {
	reg := testRegistry()
	// Build a real ContentHash via Format so the test does not hard-code
	// the on-wire separator.
	raw := sha256.Sum256([]byte("hello"))
	ch := domain.ContentHash(reg.Format("sha256", raw[:]))

	algo, want, hasher, err := artifact.ParseContentHash(reg, ch)
	if err != nil {
		t.Fatalf("ParseContentHash: %v", err)
	}
	if algo != "sha256" {
		t.Errorf("algo: got %q, want sha256", algo)
	}
	if string(want) != string(raw[:]) {
		t.Errorf("want bytes do not match the digest that produced the hash")
	}
	// The returned hasher must be fresh and usable: hashing the same input
	// reproduces the digest.
	hasher.Write([]byte("hello"))
	if string(hasher.Sum(nil)) != string(raw[:]) {
		t.Error("returned hasher did not reproduce the digest")
	}
}

func TestParseContentHash_RejectsMalformed(t *testing.T) {
	reg := testRegistry()
	if _, _, _, err := artifact.ParseContentHash(reg, domain.ContentHash("not-a-valid-hash-string")); err == nil {
		t.Fatal("expected error on malformed ContentHash")
	}
}

func TestParseContentHash_RejectsUnregisteredAlgo(t *testing.T) {
	reg := testRegistry() // only sha256 registered
	// A structurally valid "<algo>-<hex>" whose algo is not registered.
	bad := domain.ContentHash("blake3-aabbccdd")
	if _, _, _, err := artifact.ParseContentHash(reg, bad); err == nil {
		t.Fatal("expected error on unregistered algorithm")
	}
}

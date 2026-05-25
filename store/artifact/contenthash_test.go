package artifact_test

import (
	"crypto/sha256"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/internal/testutil/artifactfx"
	"scrinium.dev/store/artifact"
)

func TestParseContentHash_ValidRoundTrip(t *testing.T) {
	reg := artifactfx.Hashes()
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
	hasher.Write([]byte("hello"))
	if string(hasher.Sum(nil)) != string(raw[:]) {
		t.Error("returned hasher did not reproduce the digest")
	}
}

func TestParseContentHash_RejectsMalformed(t *testing.T) {
	if _, _, _, err := artifact.ParseContentHash(artifactfx.Hashes(), domain.ContentHash("not-a-valid-hash-string")); err == nil {
		t.Fatal("expected error on malformed ContentHash")
	}
}

func TestParseContentHash_RejectsUnregisteredAlgo(t *testing.T) {
	// artifactfx.Hashes registers only sha256; a structurally valid
	// "<algo>-<hex>" with an unregistered algo must fail at NewHasher.
	bad := domain.ContentHash("blake3-aabbccdd")
	if _, _, _, err := artifact.ParseContentHash(artifactfx.Hashes(), bad); err == nil {
		t.Fatal("expected error on unregistered algorithm")
	}
}

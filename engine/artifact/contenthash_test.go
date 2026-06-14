package artifact_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/testutil/artifactfx"
)

func TestParseContentHash_ValidRoundTrip(t *testing.T) {
	reg := artifactfx.Hashes()
	// Build a real ContentHash via Format so the test does not hard-code
	// the on-wire separator.
	raw := sha256.Sum256([]byte("hello"))
	ch := domain.ContentHash(hex.EncodeToString(raw[:]))

	want, hasher, err := artifact.ParseContentHash(reg, "sha256", ch)
	if err != nil {
		t.Fatalf("ParseContentHash: %v", err)
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
	if _, _, err := artifact.ParseContentHash(artifactfx.Hashes(), "sha256", domain.ContentHash("not-a-valid-hash-string")); err == nil {
		t.Fatal("expected error on malformed ContentHash")
	}
}

func TestParseContentHash_RejectsUnregisteredAlgo(t *testing.T) {
	// artifactfx.Hashes registers only sha256; a bare-hex ContentHash with
	// an unregistered algo must fail at NewHasher.
	bad := domain.ContentHash("aabbccdd")
	if _, _, err := artifact.ParseContentHash(artifactfx.Hashes(), "blake3", bad); err == nil {
		t.Fatal("expected error on unregistered algorithm")
	}
}

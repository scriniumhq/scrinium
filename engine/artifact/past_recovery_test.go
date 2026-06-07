package artifact_test

// Recovery tests: path edge cases that existed in the old blobpath suite but
// were not carried over. Purely additive — no shared helpers required.

import (
	"testing"

	"scrinium.dev/engine/artifact"
)

// The surviving RefFromBlobPath error tests cover an empty path, a tail with no
// algo prefix ("notaref"), and a non-hex tail ("sha256-zzzz") — but not a tail
// that is hex yet too short for the digest width.
func TestRefFromBlobPath_RejectsTooShortHex(t *testing.T) {
	if _, err := artifact.RefFromBlobPath("blobs/aa/bb/sha256-abc"); err == nil {
		t.Fatal("expected error on too-short hex tail")
	}
}

// DigestFromManifestPath had a round-trip and a malformed-segment test, but no
// explicit empty-path case.
func TestDigestFromManifestPath_RejectsEmpty(t *testing.T) {
	if _, err := artifact.DigestFromManifestPath(""); err == nil {
		t.Fatal("expected error on empty manifest path")
	}
}

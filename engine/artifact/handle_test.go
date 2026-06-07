package artifact_test

import (
	"bytes"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/testutil/artifactfx"
)

// baseManifest returns a fixture with a known ContentHash and no form
// Digest, so ComputeHandle tests start from a clean, content-controlled
// manifest.
func baseManifest() domain.Manifest {
	m := artifactfx.Manifest()
	m.ContentHash = domain.ContentHash("sha256-" + strings.Repeat("ab", 32))
	m.Digest = ""
	return m
}

// TestComputeHandle_AssignsIdentityFields: ComputeHandle populates the
// floating handle and the identity fields (meta-hash, nonce) and leaves
// the form Digest untouched — the digest is ComputeManifestDigest's job.
func TestComputeHandle_AssignsIdentityFields(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x05}, 16)
	m, err := artifact.ComputeHandle(baseManifest(), "sha256", artifactfx.Hashes(), hashing.NamingKeyPublic, nonce)
	if err != nil {
		t.Fatalf("ComputeHandle: %v", err)
	}
	if !strings.HasPrefix(string(m.ArtifactID), "sha256-") {
		t.Errorf("ArtifactID missing prefix: %q", m.ArtifactID)
	}
	if m.IdentityMetaHash == "" || !strings.HasPrefix(m.IdentityMetaHash, "sha256-") {
		t.Errorf("IdentityMetaHash not set/prefixed: %q", m.IdentityMetaHash)
	}
	if !bytes.Equal(m.IdentityNonce, nonce) {
		t.Errorf("IdentityNonce: got %x, want %x", m.IdentityNonce, nonce)
	}
	if m.Digest != "" {
		t.Errorf("ComputeHandle must not set the form Digest, got %q", m.Digest)
	}
}

// TestComputeHandle_IdentityMetaHashIsFixed: in v1 the identity-meta
// partition is empty, so IdentityMetaHash is a fixed token independent of
// the manifest's content.
func TestComputeHandle_IdentityMetaHashIsFixed(t *testing.T) {
	m1 := baseManifest()
	m1.ContentHash = domain.ContentHash("sha256-" + strings.Repeat("ab", 32))
	m2 := baseManifest()
	m2.ContentHash = domain.ContentHash("sha256-" + strings.Repeat("cd", 32))

	r1, _ := artifact.ComputeHandle(m1, "sha256", artifactfx.Hashes(), hashing.NamingKeyPublic, nil)
	r2, _ := artifact.ComputeHandle(m2, "sha256", artifactfx.Hashes(), hashing.NamingKeyPublic, nil)
	if r1.IdentityMetaHash != r2.IdentityMetaHash {
		t.Errorf("IdentityMetaHash should be content-independent: %q vs %q",
			r1.IdentityMetaHash, r2.IdentityMetaHash)
	}
}

// TestComputeHandle_UniqueVsCoalesced pins the two identity modes at the
// artifact layer: a nil nonce (Coalesced) is reproducible for the same
// content+identity, while distinct nonces (Unique) diverge even when the
// content is identical.
func TestComputeHandle_UniqueVsCoalesced(t *testing.T) {
	hashes := artifactfx.Hashes()

	// Coalesced: nil nonce, identical inputs → identical handle.
	c1, _ := artifact.ComputeHandle(baseManifest(), "sha256", hashes, hashing.NamingKeyPublic, nil)
	c2, _ := artifact.ComputeHandle(baseManifest(), "sha256", hashes, hashing.NamingKeyPublic, nil)
	if c1.ArtifactID == "" || c1.ArtifactID != c2.ArtifactID {
		t.Errorf("Coalesced handle not reproducible: %q vs %q", c1.ArtifactID, c2.ArtifactID)
	}

	// Unique: distinct nonces → distinct handles, even for identical content.
	u1, _ := artifact.ComputeHandle(baseManifest(), "sha256", hashes, hashing.NamingKeyPublic, bytes.Repeat([]byte{0x01}, 16))
	u2, _ := artifact.ComputeHandle(baseManifest(), "sha256", hashes, hashing.NamingKeyPublic, bytes.Repeat([]byte{0x02}, 16))
	if u1.ArtifactID == u2.ArtifactID {
		t.Error("Unique mode: distinct nonces produced the same handle")
	}
	if u1.ArtifactID == c1.ArtifactID {
		t.Error("Unique handle collided with the Coalesced handle")
	}
}

// TestComputeHandle_ContentAndKeySensitivity: the handle tracks the
// content digest and the naming key.
func TestComputeHandle_ContentAndKeySensitivity(t *testing.T) {
	hashes := artifactfx.Hashes()
	nonce := bytes.Repeat([]byte{0x09}, 16)

	mA := baseManifest()
	mA.ContentHash = domain.ContentHash("sha256-" + strings.Repeat("ab", 32))
	mB := baseManifest()
	mB.ContentHash = domain.ContentHash("sha256-" + strings.Repeat("cd", 32))

	base, _ := artifact.ComputeHandle(mA, "sha256", hashes, hashing.NamingKeyPublic, nonce)
	diffContent, _ := artifact.ComputeHandle(mB, "sha256", hashes, hashing.NamingKeyPublic, nonce)
	if base.ArtifactID == diffContent.ArtifactID {
		t.Error("different ContentHash produced the same handle")
	}

	diffKey, _ := artifact.ComputeHandle(mA, "sha256", hashes, []byte("scrinium/other-key/v9"), nonce)
	if base.ArtifactID == diffKey.ArtifactID {
		t.Error("different naming key produced the same handle")
	}
}

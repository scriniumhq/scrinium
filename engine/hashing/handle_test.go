package hashing

import (
	"bytes"
	"crypto/sha256"
	"hash"
	"strings"
	"testing"

	"scrinium.dev/domain"
)

// sha256Reg builds a HashRegistry with sha256 registered — the only
// algorithm Handle is exercised against here.
func sha256Reg() domain.HashRegistry {
	return NewHashRegistry().Register("sha256", func() hash.Hash { return sha256.New() })
}

var (
	cdA = domain.ContentHash("sha256-" + strings.Repeat("ab", 32))
	cdB = domain.ContentHash("sha256-" + strings.Repeat("cd", 32))
	mdA = "sha256-" + strings.Repeat("12", 32)
	mdB = "sha256-" + strings.Repeat("34", 32)
)

// TestHandle_DeterministicAndFormatted: the same (nk, cd, md, nonce)
// always yields the same handle, and the handle carries the algo prefix
// (it is produced through reg.Format).
func TestHandle_DeterministicAndFormatted(t *testing.T) {
	reg := sha256Reg()
	nonce := bytes.Repeat([]byte{0x01}, 16)

	h1, err := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, nonce)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	h2, err := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, nonce)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if h1 != h2 {
		t.Errorf("Handle not deterministic: %q vs %q", h1, h2)
	}
	if h1 == "" {
		t.Error("empty handle")
	}
	if !strings.HasPrefix(string(h1), "sha256-") {
		t.Errorf("handle missing algo prefix: %q", h1)
	}
}

// TestHandle_NonceSensitivity: in Unique mode the nonce is what makes
// each Put distinct, so different nonces must produce different handles,
// and a nil nonce must not collide with a non-nil one.
func TestHandle_NonceSensitivity(t *testing.T) {
	reg := sha256Reg()
	base, _ := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, bytes.Repeat([]byte{0x01}, 16))
	other, _ := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, bytes.Repeat([]byte{0x02}, 16))
	if base == other {
		t.Error("different nonce produced the same handle")
	}
	nilNonce, _ := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, nil)
	if nilNonce == base {
		t.Error("nil nonce collided with a non-nil nonce handle")
	}
}

// TestHandle_NilNonceReproducible: Coalesced mode passes a nil nonce —
// the handle must be reproducible from (nk, cd, md) alone, so identical
// content+identity coalesces to one ArtifactID.
func TestHandle_NilNonceReproducible(t *testing.T) {
	reg := sha256Reg()
	a, _ := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, nil)
	b, _ := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, nil)
	if a == "" || a != b {
		t.Errorf("nil-nonce handle not reproducible: %q vs %q", a, b)
	}
}

// TestHandle_InputSensitivity: each hashed input (content digest,
// identity-meta digest, naming key) must influence the handle. The nk
// case is the domain-separation guarantee — a different naming key
// reshapes the whole handle space.
func TestHandle_InputSensitivity(t *testing.T) {
	reg := sha256Reg()
	nonce := bytes.Repeat([]byte{0x07}, 16)
	base, err := Handle(reg, "sha256", NamingKeyPublic, cdA, mdA, nonce)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	cases := []struct {
		name string
		nk   []byte
		cd   domain.ContentHash
		md   string
	}{
		{"different cd", NamingKeyPublic, cdB, mdA},
		{"different md", NamingKeyPublic, cdA, mdB},
		{"different nk", []byte("scrinium/other-key/v9"), cdA, mdA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Handle(reg, "sha256", tc.nk, tc.cd, tc.md, nonce)
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got == base {
				t.Errorf("%s did not change the handle", tc.name)
			}
		})
	}
}

// TestHandle_Errors: unparseable cd or md, and an unknown algorithm, all
// surface as errors rather than a bogus handle.
func TestHandle_Errors(t *testing.T) {
	reg := sha256Reg()
	nonce := bytes.Repeat([]byte{0x01}, 16)

	if _, err := Handle(reg, "sha256", NamingKeyPublic, "not-a-hash", mdA, nonce); err == nil {
		t.Error("expected error on unparseable cd")
	}
	if _, err := Handle(reg, "sha256", NamingKeyPublic, cdA, "not-a-hash", nonce); err == nil {
		t.Error("expected error on unparseable md")
	}
	if _, err := Handle(reg, "md5", NamingKeyPublic, cdA, mdA, nonce); err == nil {
		t.Error("expected error on unknown algorithm")
	}
}

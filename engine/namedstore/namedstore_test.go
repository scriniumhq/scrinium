package namedstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"testing"

	"crypto/sha256"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/errs"
)

// testHashes is a minimal sha256-only domain.HashRegistry, local to this
// package's tests to avoid an import cycle through storefx (which pulls
// in engine/core, which depends on this package).
type testHashes struct{}

func (testHashes) Parse(h string) (string, []byte, error) {
	i := strings.IndexByte(h, '-')
	if i <= 0 {
		return "", nil, fmt.Errorf("malformed hash id %q", h)
	}
	raw, err := hex.DecodeString(h[i+1:])
	if err != nil {
		return "", nil, err
	}
	return h[:i], raw, nil
}

func (testHashes) NewHasher(algo string) (hash.Hash, error) {
	if algo == "sha256" {
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("unknown algo %q", algo)
}

func (testHashes) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

func (h testHashes) Register(string, func() hash.Hash) domain.HashRegistry { return h }

func newDriver(t *testing.T) *localfs.Driver {
	t.Helper()
	drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	return drv
}

// --- ValidateName (relocated from package store's validateSystemName) ---

func TestValidateName_Accepts(t *testing.T) {
	valid := []string{
		"config/current",
		"config/v1",
		"scrub/cursor",
		"gc/lease",
		"snapshot/2024",
		"a",
		"ingester/state/main",
	}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateName_Rejects(t *testing.T) {
	bad := map[string]string{
		"empty":            "",
		"leading slash":    "/config/current",
		"trailing slash":   "config/current/",
		"empty segment":    "config//current",
		"dot segment":      "config/./current",
		"dotdot traversal": "config/../secret",
	}
	for label, name := range bad {
		t.Run(label, func(t *testing.T) {
			if err := ValidateName(name); !errors.Is(err, errs.ErrInvalidSystemName) {
				t.Errorf("ValidateName(%q) = %v, want ErrInvalidSystemName", name, err)
			}
		})
	}
}

// --- claim / resolve / list round-trip ---

// TestClaimResolveRoundTrip exercises the relocated publish/resolve
// primitives end to end: seqs are 1-indexed and consecutive, the active
// version is max(seq), and each claimed version reads back via Load with
// verify-on-read intact.
func TestClaimResolveRoundTrip(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	const name = "scrub/cursor"

	// Absent name: no active version, empty history, no error.
	if _, found, err := ResolveActiveSeq(ctx, drv, name); err != nil || found {
		t.Fatalf("ResolveActiveSeq(absent) = (_, %v, %v), want (_, false, nil)", found, err)
	}
	if seqs, err := ListVersions(ctx, drv, name); err != nil || len(seqs) != 0 {
		t.Fatalf("ListVersions(absent) = (%v, %v), want (nil, nil)", seqs, err)
	}

	payloads := []string{"v1", "v2", "v3"}
	for i, p := range payloads {
		body, _, err := BuildInlineManifest([]byte(p), "sha256", testHashes{})
		if err != nil {
			t.Fatalf("BuildInlineManifest %s: %v", p, err)
		}
		seq, path, err := ClaimVersion(ctx, drv, name, body)
		if err != nil {
			t.Fatalf("ClaimVersion %s: %v", p, err)
		}
		if want := uint64(i + 1); seq != want {
			t.Errorf("ClaimVersion %s: seq = %d, want %d", p, seq, want)
		}
		m, err := Load(ctx, drv, testHashes{}, path)
		if err != nil {
			t.Fatalf("Load %s: %v", p, err)
		}
		if string(m.InlineBlob) != p {
			t.Errorf("Load %s: payload = %q, want %q", p, m.InlineBlob, p)
		}
	}

	seq, found, err := ResolveActiveSeq(ctx, drv, name)
	if err != nil || !found {
		t.Fatalf("ResolveActiveSeq = (%d, %v, %v), want (3, true, nil)", seq, found, err)
	}
	if seq != 3 {
		t.Errorf("active seq = %d, want 3", seq)
	}
	seqs, err := ListVersions(ctx, drv, name)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[2] != 3 {
		t.Errorf("ListVersions = %v, want [1 2 3]", seqs)
	}
}

// --- VersionPath format ---

// TestVersionPath_Format pins the on-disk path shape: root/name/seq with
// the seq zero-padded to seqWidth so lexicographic order tracks numeric
// order. An invalid name propagates ErrInvalidSystemName.
func TestVersionPath_Format(t *testing.T) {
	cases := []struct {
		name string
		seq  uint64
		want string
	}{
		{"config", 1, "system/config/0000000001"},
		{"scrub/cursor", 12, "system/scrub/cursor/0000000012"},
		{"ingester/state/main", 0, "system/ingester/state/main/0000000000"},
	}
	for _, c := range cases {
		got, err := VersionPath(c.name, c.seq)
		if err != nil {
			t.Fatalf("VersionPath(%q, %d): %v", c.name, c.seq, err)
		}
		if got != c.want {
			t.Errorf("VersionPath(%q, %d) = %q, want %q", c.name, c.seq, got, c.want)
		}
	}

	if _, err := VersionPath("a//b", 1); !errors.Is(err, errs.ErrInvalidSystemName) {
		t.Errorf("VersionPath(invalid) err = %v, want ErrInvalidSystemName", err)
	}
}

// --- verify-on-read ---

// TestLoad_RejectsTamperedPayload covers the integrity anchor: a version
// file whose inline payload no longer matches its embedded ContentHash
// must fail Load with ErrCorruptedContent. We build a valid manifest,
// flip a payload byte, and re-encode WITHOUT recomputing the hash, then
// write the bytes straight to a version path.
func TestLoad_RejectsTamperedPayload(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)

	_, m, err := BuildInlineManifest([]byte("real-payload"), "sha256", testHashes{})
	if err != nil {
		t.Fatalf("BuildInlineManifest: %v", err)
	}
	m.InlineBlob = append([]byte(nil), m.InlineBlob...)
	m.InlineBlob[0] ^= 0xff // ContentHash left untouched → now inconsistent

	_, fileBytes, _, err := artifact.ComputeManifestDigest(
		m, "sha256", testHashes{},
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatalf("encode tampered manifest: %v", err)
	}

	path, err := VersionPath("scrub/cursor", 1)
	if err != nil {
		t.Fatalf("VersionPath: %v", err)
	}
	if err := drv.Put(ctx, path, bytes.NewReader(fileBytes)); err != nil {
		t.Fatalf("seed tampered version: %v", err)
	}

	if _, err := Load(ctx, drv, testHashes{}, path); !errors.Is(err, errs.ErrCorruptedContent) {
		t.Fatalf("Load tampered: got %v, want ErrCorruptedContent", err)
	}
}

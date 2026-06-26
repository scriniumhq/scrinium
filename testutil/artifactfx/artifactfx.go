package artifactfx

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"hash"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/testutil/manifestfx"
)

// Hashes returns a HashRegistry with sha256 registered, sufficient for
// format round-trip and ID-computation tests. (storefx.Hashes adds a
// blake3 alias for store tests that exercise a second algorithm; format
// tests need only one real hasher.)
func Hashes() domain.HashRegistry {
	return hashing.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// DEK returns a deterministic 32-byte data-encryption key for tests. The
// fixed fill makes encrypted-mode round-trips reproducible; never use this
// outside tests.
func DEK() []byte {
	return bytes.Repeat([]byte{0x42}, 32)
}

// Payload wraps a string as a domain.Artifact body. The single most
// common test shape: a literal payload to Put. Lives here (not in
// domain) on purpose — production feeds Artifact.Payload a streaming
// io.Reader from a file/socket/HTTP body; wrapping an in-memory literal
// is a test convenience, and putting it in the domain leaf would invite
// buffering whole blobs in memory against the streaming design.
func Payload(s string) domain.Artifact {
	return domain.Artifact{Payload: strings.NewReader(s)}
}

// PayloadBytes wraps a byte slice as a domain.Artifact body — for binary
// fixtures and table tests that build []byte directly.
func PayloadBytes(b []byte) domain.Artifact {
	return domain.Artifact{Payload: bytes.NewReader(b)}
}

// PayloadSized returns an Artifact whose body is n deterministic bytes,
// for tests of inline limits, segmentation, and dedup boundaries. The
// content is a fixed repeating pattern, not random: a failing size test
// must reproduce byte-for-byte across runs.
func PayloadSized(n int) domain.Artifact {
	return PayloadBytes(SizedBytes(n))
}

// SizedBytes returns n deterministic bytes (a repeating 0..255 ramp).
// Exposed for callers that need the raw slice rather than an Artifact.
func SizedBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 256)
	}
	return b
}

// Keys returns a KeyProvider that hands out the given candidate DEKs for
// any KeyID. Each call returns fresh copies, matching production resolvers
// (which give defensive copies that the codec wipes after use). With no
// arguments it defaults to a single DEK().
func Keys(deks ...[]byte) artifact.KeyProvider {
	if len(deks) == 0 {
		deks = [][]byte{DEK()}
	}
	return fakeKeys{deks: deks}
}

type fakeKeys struct{ deks [][]byte }

func (f fakeKeys) GetKeys(string) ([][]byte, error) {
	out := make([][]byte, len(f.deks))
	for i, k := range f.deks {
		out[i] = append([]byte(nil), k...)
	}
	return out, nil
}

// Manifest returns a valid blob manifest carrying ext, usr, and an inline
// blob — so encrypted-mode tests have content to hide and verify — built
// on top of manifestfx.Sample (which owns the sys-field defaults). Apply
// mutators to tweak fields for a specific case.
//
//	m := artifactfx.Manifest()                       // full sample
//	m := artifactfx.Manifest(func(m *domain.Manifest) { m.Namespace = "x" })
func Manifest(mutators ...func(*domain.Manifest)) domain.Manifest {
	m := manifestfx.Sample()
	// Sample() is a bare blob body with an empty identity slot; fill the
	// handle slot together with its identity-meta so this kitchen-sink
	// round-trip fixture is a structurally valid user artifact and the
	// encode boundary accepts it (ADR-104 validateSlot).
	m.ArtifactID = domain.ArtifactID(strings.Repeat("e", 64))
	m.IdentityMetaHash = "sha256-" + strings.Repeat("c", 64)
	m.Pipeline = []domain.PipelineStage{}
	m.Ext = json.RawMessage(`{"k":"ext-value"}`)
	m.Usr = json.RawMessage(`{"u":"usr-value"}`)
	m.InlineBlob = []byte("inline-secret-bytes")
	for _, fn := range mutators {
		fn(&m)
	}
	return m
}

// Encoded computes the on-disk file bytes for m under the given crypto
// mode and returns the ManifestDigest (the file's hash = on-disk name =
// form-verifier) plus the bytes.
//
// The floating handle is computed (deterministically, with a nil identity
// nonce) and embedded in the body, but the returned id is the digest —
// that is what verify and path tests assert against. Plain ignores
// dek/keyID; Sealed/Paranoid use DEK() and a fixed KeyID. It fails the
// test on any encode error, so call sites stay terse.
func Encoded(t testing.TB, m domain.Manifest, crypto domain.ManifestCrypto) (domain.ManifestDigest, []byte) {
	t.Helper()
	var dek []byte
	var keyID string
	if crypto != "" && crypto != domain.ManifestCryptoPlain {
		dek = DEK()
		keyID = "k1"
	}
	wh, err := artifact.ComputeHandle(m, "sha256", Hashes(), hashing.NamingKeyPublic, nil)
	if err != nil {
		t.Fatalf("artifactfx.Encoded(%s): handle: %v", crypto, err)
	}
	digest, b, _, err := artifact.ComputeManifestDigest(wh, "sha256", Hashes(), domain.ManifestEncodingJSON, crypto, dek, keyID)
	if err != nil {
		t.Fatalf("artifactfx.Encoded(%s): %v", crypto, err)
	}
	return digest, b
}

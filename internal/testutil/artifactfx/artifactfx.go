package artifactfx

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"hash"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/internal/testutil/manifestfx"
	"scrinium.dev/store/artifact"
	"scrinium.dev/store/hashing"
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
// mode, returning the assigned ArtifactID and the bytes. Plain ignores
// dek/keyID; Sealed/Paranoid use DEK() and a fixed KeyID. It fails the
// test on any encode error, so call sites stay terse.
func Encoded(t testing.TB, m domain.Manifest, crypto domain.ManifestCrypto) (domain.ArtifactID, []byte) {
	t.Helper()
	var dek []byte
	var keyID string
	if crypto != "" && crypto != domain.ManifestCryptoPlain {
		dek = DEK()
		keyID = "k1"
	}
	id, b, _, err := artifact.ComputeArtifactID(m, "sha256", Hashes(), domain.ManifestEncodingJSON, crypto, dek, keyID)
	if err != nil {
		t.Fatalf("artifactfx.Encoded(%s): %v", crypto, err)
	}
	return id, b
}

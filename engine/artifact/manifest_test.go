package artifact_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"testing"
	"time"

	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/hashing"
)

func reg() domain.HashRegistry {
	return hashing.NewHashRegistry().Register("sha256", func() hash.Hash { return sha256.New() })
}

// sampleManifest returns a Plain blob manifest with ext/usr/inline set so
// the crypto-mode tests have something to hide.
func sampleManifest() domain.Manifest {
	return domain.Manifest{
		Type:         domain.ManifestTypeBlob,
		Namespace:    "ns",
		SessionID:    "sess-1",
		ContentHash:  domain.ContentHash("sha256-" + repeat("a", 64)),
		BlobRef:      domain.BlobRef("sha256-" + repeat("a", 64)),
		OriginalSize: 1234,
		CreatedAt:    time.Unix(1700000000, 0).UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Pipeline:     []domain.PipelineStage{},
		Ext:          json.RawMessage(`{"k":"ext-value"}`),
		Usr:          json.RawMessage(`{"u":"usr-value"}`),
		InlineBlob:   []byte("inline-secret-bytes"),
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s[0])
	}
	return string(out)
}

// fakeKeys is a KeyProvider returning fixed candidate DEKs.
type fakeKeys struct{ keys [][]byte }

func (f fakeKeys) GetKeys(string) ([][]byte, error) {
	// hand out copies (resolvers give defensive copies; DecodeEncrypted wipes them)
	out := make([][]byte, len(f.keys))
	for i, k := range f.keys {
		out[i] = append([]byte(nil), k...)
	}
	return out, nil
}

func dek32() []byte { return bytes.Repeat([]byte{0x42}, 32) }

// --- Plain: encode/decode round-trip ---

func TestEncodeDecode_PlainRoundTrip(t *testing.T) {
	m := sampleManifest()
	b, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != m.Namespace || string(got.BlobRef) != string(m.BlobRef) || got.OriginalSize != m.OriginalSize {
		t.Errorf("round-trip lost sys fields: %+v", got)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("inline blob lost: got %q", got.InlineBlob)
	}
}

func TestEncode_Deterministic(t *testing.T) {
	m := sampleManifest()
	a, _ := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	b, _ := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if !bytes.Equal(a, b) {
		t.Error("Encode is not deterministic for the same manifest")
	}
}

func TestEncode_RejectsNonPlain(t *testing.T) {
	if _, err := artifact.Encode(sampleManifest(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed); !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("want ErrUnsupportedCrypto, got %v", err)
	}
}

func TestDecode_RejectsEncrypted(t *testing.T) {
	// Build a Sealed file, then try the Plain Decode on it.
	_, b, _, err := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, dek32(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, derr := artifact.Decode(b); !errors.Is(derr, errs.ErrUnsupportedCrypto) {
		t.Fatalf("Decode on encrypted file: want ErrUnsupportedCrypto, got %v", derr)
	}
}

// --- ComputeArtifactID: stability + uniqueness + assignment ---

func TestComputeArtifactID_StableAndAssigned(t *testing.T) {
	m := sampleManifest()
	id1, b1, sm, err := artifact.ComputeArtifactID(m, "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("empty ArtifactID")
	}
	if sm.ArtifactID != id1 {
		t.Errorf("returned manifest ArtifactID=%q, want %q", sm.ArtifactID, id1)
	}
	// Stable: same manifest → same id and bytes.
	id2, b2, _, _ := artifact.ComputeArtifactID(m, "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if id1 != id2 || !bytes.Equal(b1, b2) {
		t.Error("ComputeArtifactID not stable across calls")
	}
}

func TestComputeArtifactID_DifferentManifestsDifferentIDs(t *testing.T) {
	m1 := sampleManifest()
	m2 := sampleManifest()
	m2.Namespace = "other"
	id1, _, _, _ := artifact.ComputeArtifactID(m1, "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	id2, _, _, _ := artifact.ComputeArtifactID(m2, "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if id1 == id2 {
		t.Error("different manifests produced the same ArtifactID")
	}
}

// --- VerifyArtifactID: tamper detection ---

func TestVerifyArtifactID_AcceptsUntampered(t *testing.T) {
	id, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if err := artifact.VerifyArtifactID(id, b, reg()); err != nil {
		t.Fatalf("verify untampered: %v", err)
	}
}

func TestVerifyArtifactID_DetectsTampering(t *testing.T) {
	id, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	b[len(b)-1] ^= 0xff // flip a byte
	if err := artifact.VerifyArtifactID(id, b, reg()); !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Fatalf("want ErrCorruptedManifest, got %v", err)
	}
}

// --- Sealed: round-trip + sys stays plaintext ---

func TestSealed_RoundTrip(t *testing.T) {
	m := sampleManifest()
	_, b, _, err := artifact.ComputeArtifactID(m, "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, dek32(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.DecodeEncrypted(b, fakeKeys{keys: [][]byte{dek32()}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("inline blob did not survive Sealed round-trip: %q", got.InlineBlob)
	}
	if !bytes.Equal(got.Ext, m.Ext) {
		t.Errorf("ext did not survive: %s", got.Ext)
	}
}

func TestSealed_SysFieldsArePlaintextOnDisk(t *testing.T) {
	_, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, dek32(), "k1")
	// Namespace lives in sys, which Sealed leaves in clear JSON.
	if !bytes.Contains(b, []byte(`"ns"`)) {
		t.Error("Sealed should leave sys (namespace) in plaintext on disk")
	}
	// The inline secret must NOT appear in clear.
	if bytes.Contains(b, []byte("inline-secret-bytes")) {
		t.Error("Sealed leaked inline_blob plaintext on disk")
	}
}

// --- Paranoid: round-trip + everything hidden ---

func TestParanoid_RoundTrip(t *testing.T) {
	m := sampleManifest()
	_, b, _, err := artifact.ComputeArtifactID(m, "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoParanoid, dek32(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.DecodeEncrypted(b, fakeKeys{keys: [][]byte{dek32()}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != m.Namespace || !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Error("Paranoid round-trip lost fields")
	}
}

func TestParanoid_HidesSysOnDisk(t *testing.T) {
	_, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoParanoid, dek32(), "k1")
	if bytes.Contains(b, []byte(`"ns"`)) {
		t.Error("Paranoid should encrypt the whole body including sys (namespace)")
	}
}

// --- DecodeEncrypted: failure classes ---

func TestDecodeEncrypted_WrongKeyFails(t *testing.T) {
	_, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, dek32(), "k1")
	wrong := bytes.Repeat([]byte{0x99}, 32)
	if _, err := artifact.DecodeEncrypted(b, fakeKeys{keys: [][]byte{wrong}}); !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("want ErrDecryptionFailed, got %v", err)
	}
}

func TestDecodeEncrypted_NoResolverOnEncrypted(t *testing.T) {
	_, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoParanoid, dek32(), "k1")
	if _, err := artifact.DecodeEncrypted(b, nil); !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeEncrypted_EmptyCandidates(t *testing.T) {
	_, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, dek32(), "k1")
	if _, err := artifact.DecodeEncrypted(b, fakeKeys{keys: nil}); !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeEncrypted_PlainForwards(t *testing.T) {
	// A Plain file goes through DecodeEncrypted with a nil resolver fine.
	_, b, _, _ := artifact.ComputeArtifactID(sampleManifest(), "sha256", reg(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if _, err := artifact.DecodeEncrypted(b, nil); err != nil {
		t.Fatalf("Plain via DecodeEncrypted should succeed: %v", err)
	}
}

package artifact_test

// Recovery tests: coverage that existed in the old manifestcodec/encrypted
// test suites but was lost or weakened during the artifact extraction.
//
// This file is deliberately self-contained — it defines its own fixtures
// (rec* helpers) instead of reusing the package-level ones, so it can be
// added without touching any existing _test.go file. The strong full-field
// round-trip tests here supersede the weaker originals; removing those
// originals (and folding these helpers into the shared fixture) is left to
// the subtractive follow-up PR.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"hash"
	"testing"
	"time"

	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/hashing"
)

func recReg() domain.HashRegistry {
	return hashing.NewHashRegistry().Register("sha256", func() hash.Hash { return sha256.New() })
}

func recDEK() []byte { return bytes.Repeat([]byte{0x42}, 32) }

// recKeys is a KeyProvider handing out defensive copies of fixed DEKs.
type recKeys struct{ keys [][]byte }

func (k recKeys) GetKeys(string) ([][]byte, error) {
	out := make([][]byte, len(k.keys))
	for i, kk := range k.keys {
		out[i] = append([]byte(nil), kk...)
	}
	return out, nil
}

func recHex(b byte, n int) string {
	return "sha256-" + string(bytes.Repeat([]byte{b}, n))
}

// recManifest is a Plain blob manifest with ext/usr/inline populated, mirroring
// the package fixture so the crypto-mode tests have something to hide.
func recManifest(mut ...func(*domain.Manifest)) domain.Manifest {
	m := domain.Manifest{
		Type:         domain.ManifestTypeBlob,
		Namespace:    "ns",
		SessionID:    "sess-1",
		ContentHash:  domain.ContentHash(recHex('a', 64)),
		BlobRef:      domain.BlobRef(recHex('a', 64)),
		OriginalSize: 1234,
		CreatedAt:    time.Unix(1700000000, 0).UTC(),
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
		Pipeline:     []domain.PipelineStage{},
		Ext:          json.RawMessage(`{"k":"ext-value"}`),
		Usr:          json.RawMessage(`{"u":"usr-value"}`),
		InlineBlob:   []byte("inline-secret-bytes"),
	}
	for _, f := range mut {
		f(&m)
	}
	return m
}

// --- Plain: full-field round-trip (supersedes the sys-only PlainRoundTrip) ---

func TestEncodeDecode_PlainRoundTrip_AllFields(t *testing.T) {
	m := recManifest()
	b, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != m.Type {
		t.Errorf("Type lost: got %q want %q", got.Type, m.Type)
	}
	if got.Namespace != m.Namespace || got.SessionID != m.SessionID {
		t.Errorf("namespace/session lost: %+v", got)
	}
	if string(got.ContentHash) != string(m.ContentHash) || string(got.BlobRef) != string(m.BlobRef) {
		t.Errorf("content/blob ref lost: %+v", got)
	}
	if got.OriginalSize != m.OriginalSize {
		t.Errorf("OriginalSize lost: got %d want %d", got.OriginalSize, m.OriginalSize)
	}
	if !got.CreatedAt.Equal(m.CreatedAt) {
		t.Errorf("CreatedAt lost: got %v want %v", got.CreatedAt, m.CreatedAt)
	}
	if !bytes.Equal(got.Ext, m.Ext) {
		t.Errorf("Ext lost: got %s", got.Ext)
	}
	if !bytes.Equal(got.Usr, m.Usr) {
		t.Errorf("Usr lost: got %s", got.Usr)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("InlineBlob lost: got %q", got.InlineBlob)
	}
}

// --- Plain: RetentionUntil round-trip (was untested anywhere) ---

func TestEncodeDecode_RetentionRoundTrip(t *testing.T) {
	want := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	m := recManifest(func(m *domain.Manifest) { m.RetentionUntil = want })
	b, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RetentionUntil.Equal(want) {
		t.Errorf("RetentionUntil did not survive round-trip: got %v want %v", got.RetentionUntil, want)
	}
}

// --- Plain: ext+usr are plaintext on disk (sealed/paranoid contrast) ---

func TestEncode_PlainExtUsrVisibleOnDisk(t *testing.T) {
	m := recManifest(func(m *domain.Manifest) {
		m.Ext = json.RawMessage(`{"m":"EXTMARKER_7f3a"}`)
		m.Usr = json.RawMessage(`{"m":"USRMARKER_91b2"}`)
	})
	b, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("EXTMARKER_7f3a")) {
		t.Error("Plain should leave ext in plaintext on disk")
	}
	if !bytes.Contains(b, []byte("USRMARKER_91b2")) {
		t.Error("Plain should leave usr in plaintext on disk")
	}
}

// --- Sealed: full-field round-trip (supersedes the inline+ext-only version) ---

func TestSealed_RoundTrip_AllFields(t *testing.T) {
	m := recManifest()
	_, b, _, err := artifact.ComputeArtifactID(m, "sha256", recReg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, recDEK(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.DecodeEncrypted(b, recKeys{keys: [][]byte{recDEK()}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != m.Namespace {
		t.Errorf("Namespace lost: got %q", got.Namespace)
	}
	if string(got.BlobRef) != string(m.BlobRef) {
		t.Errorf("BlobRef lost: got %q", got.BlobRef)
	}
	if !bytes.Equal(got.Ext, m.Ext) {
		t.Errorf("Ext lost: got %s", got.Ext)
	}
	if !bytes.Equal(got.Usr, m.Usr) {
		t.Errorf("Usr lost: got %s", got.Usr)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("InlineBlob lost: got %q", got.InlineBlob)
	}
}

// --- Sealed: nil ext/usr survive as empty (edge case) ---

func TestSealed_EmptyExtAndUsr(t *testing.T) {
	m := recManifest(func(m *domain.Manifest) {
		m.Ext = nil
		m.Usr = nil
	})
	_, b, _, err := artifact.ComputeArtifactID(m, "sha256", recReg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, recDEK(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.DecodeEncrypted(b, recKeys{keys: [][]byte{recDEK()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ext) != 0 {
		t.Errorf("expected empty Ext, got %s", got.Ext)
	}
	if len(got.Usr) != 0 {
		t.Errorf("expected empty Usr, got %s", got.Usr)
	}
}

// --- Paranoid: full-field round-trip (adds ext/usr equality) ---

func TestParanoid_RoundTrip_AllFields(t *testing.T) {
	m := recManifest()
	_, b, _, err := artifact.ComputeArtifactID(m, "sha256", recReg(), domain.ManifestEncodingJSON, domain.ManifestCryptoParanoid, recDEK(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.DecodeEncrypted(b, recKeys{keys: [][]byte{recDEK()}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != m.Namespace {
		t.Errorf("Namespace lost: got %q", got.Namespace)
	}
	if !bytes.Equal(got.Ext, m.Ext) {
		t.Errorf("Ext lost: got %s", got.Ext)
	}
	if !bytes.Equal(got.Usr, m.Usr) {
		t.Errorf("Usr lost: got %s", got.Usr)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("InlineBlob lost: got %q", got.InlineBlob)
	}
}

// --- Paranoid: ext+usr (not just sys) are encrypted on disk ---

func TestParanoid_HidesExtAndUsr(t *testing.T) {
	m := recManifest(func(m *domain.Manifest) {
		m.Ext = json.RawMessage(`{"m":"EXTMARKER_7f3a"}`)
		m.Usr = json.RawMessage(`{"m":"USRMARKER_91b2"}`)
	})
	_, b, _, err := artifact.ComputeArtifactID(m, "sha256", recReg(), domain.ManifestEncodingJSON, domain.ManifestCryptoParanoid, recDEK(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("EXTMARKER_7f3a")) {
		t.Error("Paranoid leaked ext plaintext on disk")
	}
	if bytes.Contains(b, []byte("USRMARKER_91b2")) {
		t.Error("Paranoid leaked usr plaintext on disk")
	}
}

// --- Pipeline: multi-stage round-trip incl. IV + KeyID co-presence ---
//
// The surviving single-stage KeyID test does not exercise IV round-trip, nor
// that non-crypto stages omit key_id while crypto stages carry it.

func TestEncodeDecode_PipelineMultiStageRoundTrip(t *testing.T) {
	m := recManifest(func(m *domain.Manifest) {
		m.Pipeline = []domain.PipelineStage{
			{Algorithm: "zstd", Hash: recHex('c', 64)},
			{Algorithm: "aes-gcm", Hash: recHex('d', 64), IV: []byte{1, 2, 3, 4}, KeyID: "tenant-42"},
		}
	})
	b, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pipeline) != 2 {
		t.Fatalf("expected 2 pipeline stages, got %d", len(got.Pipeline))
	}
	if got.Pipeline[0].Algorithm != "zstd" {
		t.Errorf("stage 0 algorithm: got %q", got.Pipeline[0].Algorithm)
	}
	if len(got.Pipeline[0].IV) != 0 || got.Pipeline[0].KeyID != "" {
		t.Errorf("non-crypto stage should omit iv/key_id, got iv=%x key_id=%q", got.Pipeline[0].IV, got.Pipeline[0].KeyID)
	}
	if got.Pipeline[1].Algorithm != "aes-gcm" {
		t.Errorf("stage 1 algorithm: got %q", got.Pipeline[1].Algorithm)
	}
	if !bytes.Equal(got.Pipeline[1].IV, []byte{1, 2, 3, 4}) {
		t.Errorf("stage 1 IV did not survive: got %x", got.Pipeline[1].IV)
	}
	if got.Pipeline[1].KeyID != "tenant-42" {
		t.Errorf("stage 1 KeyID did not survive: got %q", got.Pipeline[1].KeyID)
	}
}

// --- ComputeArtifactID rejects an empty DEK when crypto requires one ---

func TestComputeArtifactID_RejectsEmptyDEKOnEncrypted(t *testing.T) {
	if _, _, _, err := artifact.ComputeArtifactID(recManifest(), "sha256", recReg(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed, nil, ""); err == nil {
		t.Fatal("Sealed encode with empty DEK must be rejected")
	}
}

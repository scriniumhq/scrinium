package artifact_test

import (
	"bytes"
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
)

// --- Plain: encode/decode round-trip ---

func TestEncodeDecode_PlainRoundTrip(t *testing.T) {
	m := artifactfx.Manifest()
	b, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != m.Namespace || string(got.PrimaryBlobRef()) != string(m.PrimaryBlobRef()) || got.OriginalSize != m.OriginalSize {
		t.Errorf("round-trip lost sys fields: %+v", got)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("inline blob lost: got %q", got.InlineBlob)
	}
}

func TestEncode_Deterministic(t *testing.T) {
	m := artifactfx.Manifest()
	a, _ := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	b, _ := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if !bytes.Equal(a, b) {
		t.Error("Encode is not deterministic for the same manifest")
	}
}

func TestEncode_RejectsNonPlain(t *testing.T) {
	if _, err := artifact.Encode(artifactfx.Manifest(), domain.ManifestEncodingJSON, domain.ManifestCryptoSealed); !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("want ErrUnsupportedCrypto, got %v", err)
	}
}

func TestDecode_RejectsEncrypted(t *testing.T) {
	_, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoSealed)
	if _, derr := artifact.Decode(b); !errors.Is(derr, errs.ErrUnsupportedCrypto) {
		t.Fatalf("Decode on encrypted file: want ErrUnsupportedCrypto, got %v", derr)
	}
}

// --- ComputeManifestDigest: stability + uniqueness + assignment ---

func TestComputeManifestDigest_StableAndAssigned(t *testing.T) {
	m := artifactfx.Manifest()
	dg1, b1, sm, err := artifact.ComputeManifestDigest(m, "sha256", artifactfx.Hashes(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if dg1 == "" {
		t.Fatal("empty ManifestDigest")
	}
	if sm.Digest != dg1 {
		t.Errorf("returned manifest Digest=%q, want %q", sm.Digest, dg1)
	}
	dg2, b2, _, _ := artifact.ComputeManifestDigest(m, "sha256", artifactfx.Hashes(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if dg1 != dg2 || !bytes.Equal(b1, b2) {
		t.Error("ComputeManifestDigest not stable across calls")
	}
}

func TestComputeManifestDigest_DifferentManifestsDifferentIDs(t *testing.T) {
	m1 := artifactfx.Manifest()
	m2 := artifactfx.Manifest(func(m *domain.Manifest) { m.Namespace = "other" })
	dg1, _, _, _ := artifact.ComputeManifestDigest(m1, "sha256", artifactfx.Hashes(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	dg2, _, _, _ := artifact.ComputeManifestDigest(m2, "sha256", artifactfx.Hashes(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if dg1 == dg2 {
		t.Error("different manifests produced the same ManifestDigest")
	}
}

// --- VerifyManifestDigest: tamper detection ---

func TestVerifyManifestDigest_AcceptsUntampered(t *testing.T) {
	id, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoPlain)
	if err := artifact.VerifyManifestDigest(id, b, "sha256", artifactfx.Hashes()); err != nil {
		t.Fatalf("verify untampered: %v", err)
	}
}

func TestVerifyManifestDigest_DetectsTampering(t *testing.T) {
	id, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoPlain)
	b[len(b)-1] ^= 0xff
	if err := artifact.VerifyManifestDigest(id, b, "sha256", artifactfx.Hashes()); !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Fatalf("want ErrCorruptedManifest, got %v", err)
	}
}

// --- Sealed: round-trip + sys stays plaintext ---

func TestSealed_RoundTrip(t *testing.T) {
	m := artifactfx.Manifest()
	_, b := artifactfx.Encoded(t, m, domain.ManifestCryptoSealed)
	got, err := artifact.DecodeEncrypted(b, artifactfx.Keys())
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
	m := artifactfx.Manifest(func(m *domain.Manifest) { m.Namespace = "ns" })
	_, b := artifactfx.Encoded(t, m, domain.ManifestCryptoSealed)
	if !bytes.Contains(b, []byte(`"ns"`)) {
		t.Error("Sealed should leave sys (namespace) in plaintext on disk")
	}
	if bytes.Contains(b, []byte("inline-secret-bytes")) {
		t.Error("Sealed leaked inline_blob plaintext on disk")
	}
}

// --- Paranoid: round-trip + everything hidden ---

func TestParanoid_RoundTrip(t *testing.T) {
	m := artifactfx.Manifest()
	_, b := artifactfx.Encoded(t, m, domain.ManifestCryptoParanoid)
	got, err := artifact.DecodeEncrypted(b, artifactfx.Keys())
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != m.Namespace || !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Error("Paranoid round-trip lost fields")
	}
}

func TestParanoid_HidesSysOnDisk(t *testing.T) {
	m := artifactfx.Manifest(func(m *domain.Manifest) { m.Namespace = "ns" })
	_, b := artifactfx.Encoded(t, m, domain.ManifestCryptoParanoid)
	if bytes.Contains(b, []byte(`"ns"`)) {
		t.Error("Paranoid should encrypt the whole body including sys (namespace)")
	}
}

// --- DecodeEncrypted: failure classes ---

func TestDecodeEncrypted_WrongKeyFails(t *testing.T) {
	_, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoSealed)
	wrong := bytes.Repeat([]byte{0x99}, 32)
	if _, err := artifact.DecodeEncrypted(b, artifactfx.Keys(wrong)); !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("want ErrDecryptionFailed, got %v", err)
	}
}

func TestDecodeEncrypted_NoResolverOnEncrypted(t *testing.T) {
	_, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoParanoid)
	if _, err := artifact.DecodeEncrypted(b, nil); !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeEncrypted_EmptyCandidates(t *testing.T) {
	_, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoSealed)
	if _, err := artifact.DecodeEncrypted(b, emptyKeys{}); !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeEncrypted_PlainForwards(t *testing.T) {
	_, b := artifactfx.Encoded(t, artifactfx.Manifest(), domain.ManifestCryptoPlain)
	if _, err := artifact.DecodeEncrypted(b, nil); err != nil {
		t.Fatalf("Plain via DecodeEncrypted should succeed: %v", err)
	}
}

func TestEncode_ManifestTooLarge(t *testing.T) {
	huge := make([]byte, domain.MaxManifestSize)
	huge[0] = '"'
	for i := 1; i < len(huge)-1; i++ {
		huge[i] = 'x'
	}
	huge[len(huge)-1] = '"'

	m := artifactfx.Manifest(func(m *domain.Manifest) { m.Ext = huge })

	_, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if !errors.Is(err, errs.ErrManifestTooLarge) {
		t.Fatalf("Encode: got %v, want errs.ErrManifestTooLarge", err)
	}
}

func TestEncode_ManifestUnderLimit_OK(t *testing.T) {
	huge := make([]byte, domain.MaxManifestSize/2)
	huge[0] = '"'
	for i := 1; i < len(huge)-1; i++ {
		huge[i] = 'x'
	}
	huge[len(huge)-1] = '"'

	m := artifactfx.Manifest(func(m *domain.Manifest) { m.Ext = huge })

	bs, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(bs) > domain.MaxManifestSize {
		t.Fatalf("encoded size %d exceeds limit %d", len(bs), domain.MaxManifestSize)
	}
}

func TestEncodeDecode_PipelineKeyIDRoundTrip(t *testing.T) {
	m := artifactfx.Manifest(func(m *domain.Manifest) {
		m.Pipeline = []domain.PipelineStage{
			{
				Algorithm: "aes-gcm",
				Hash:      "sha256-abcdef",
				IV:        []byte{0x10, 0x20, 0x30},
				KeyID:     "tenant-42",
			},
		}
	})
	bs, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := artifact.Decode(bs)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Pipeline[0].KeyID != "tenant-42" {
		t.Errorf("crypto stage KeyID: got %q, want %q", got.Pipeline[0].KeyID, "tenant-42")
	}
}

// emptyKeys resolves to zero candidates, exercising the ErrKeyNotFound
// branch that artifactfx.Keys (which always returns at least one) cannot.
type emptyKeys struct{}

func (emptyKeys) GetKeys(string) ([][]byte, error) { return nil, nil }

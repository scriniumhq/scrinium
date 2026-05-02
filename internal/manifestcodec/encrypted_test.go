package manifestcodec_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/manifestcodec"
)

// freshDEK returns a 32-byte DEK from crypto/rand.
func freshDEK(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return b
}

// staticKeyProvider is a minimal in-test implementation of
// manifestcodec.KeyProvider.
type staticKeyProvider struct {
	byKeyID map[string][][]byte
}

func (s *staticKeyProvider) GetKeys(keyID string) ([][]byte, error) {
	keys := s.byKeyID[keyID]
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = append([]byte{}, k...)
	}
	return out, nil
}

// resolverWith builds a single-keyID resolver.
func resolverWith(keyID string, dek []byte) *staticKeyProvider {
	return &staticKeyProvider{byKeyID: map[string][][]byte{keyID: {dek}}}
}

// --- MetadataOnly round-trip ---

func TestMetadataOnly_RoundTrip(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Metadata = json.RawMessage(`{"tenant":"acme","tags":["a","b"]}`)

	bs, err := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoMetadataOnly, dek, "")
	if err != nil {
		t.Fatalf("EncodeFileEncrypted: %v", err)
	}

	got, err := manifestcodec.DecodeFileEncrypted(bs, resolverWith("", dek))
	if err != nil {
		t.Fatalf("DecodeFileEncrypted: %v", err)
	}

	if !bytes.Equal([]byte(got.Metadata), []byte(src.Metadata)) {
		t.Errorf("metadata round-trip: got %s, want %s",
			string(got.Metadata), string(src.Metadata))
	}
	if got.Namespace != src.Namespace {
		t.Errorf("Namespace: got %q, want %q", got.Namespace, src.Namespace)
	}
	if got.BlobRef != src.BlobRef {
		t.Errorf("BlobRef: got %q, want %q", got.BlobRef, src.BlobRef)
	}
}

func TestMetadataOnly_SystemFieldsArePlaintext(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Namespace = "tenant-a/orders"
	src.Metadata = json.RawMessage(`{"secret":"do-not-leak"}`)

	bs, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoMetadataOnly, dek, "")

	// Without any decryption, body should still contain the
	// namespace string in plaintext (system fields stay open).
	if !bytes.Contains(bs, []byte("tenant-a/orders")) {
		t.Error("MetadataOnly should leave Namespace in plaintext on disk")
	}
	// And the metadata content must NOT be visible.
	if bytes.Contains(bs, []byte("do-not-leak")) {
		t.Error("MetadataOnly leaked metadata to plaintext")
	}
}

func TestMetadataOnly_EmptyMetadata(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Metadata = nil

	bs, err := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoMetadataOnly, dek, "")
	if err != nil {
		t.Fatalf("EncodeFileEncrypted: %v", err)
	}
	got, err := manifestcodec.DecodeFileEncrypted(bs, resolverWith("", dek))
	if err != nil {
		t.Fatalf("DecodeFileEncrypted: %v", err)
	}
	if len(got.Metadata) != 0 {
		t.Errorf("metadata: expected empty, got %s", string(got.Metadata))
	}
}

func TestMetadataOnly_TamperedHeaderFailsDecryption(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoMetadataOnly, dek, "tenant-a")

	// Find the KeyID byte and flip one bit. The 'a' in
	// "tenant-a" is right after the 1-byte length.
	idx := bytes.Index(bs, []byte("tenant-a"))
	if idx < 0 {
		t.Fatal("test setup: KeyID not found in header")
	}
	tampered := append([]byte{}, bs...)
	tampered[idx] = 'X'

	_, err := manifestcodec.DecodeFileEncrypted(tampered, resolverWith("Xenant-a", dek))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed (header AAD mismatch), got %v", err)
	}
}

func TestMetadataOnly_TamperedCiphertext(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoMetadataOnly, dek, "")

	// MetadataOnly stores ciphertext as base64 inside a JSON
	// string. Find the JSON metadata key and replace one
	// character of the base64 with another base64-valid one.
	idx := bytes.Index(bs, []byte(`"metadata":"`))
	if idx < 0 {
		t.Skip("metadata key not found at expected JSON position")
	}
	// First base64 character after the opening quote.
	pos := idx + len(`"metadata":"`)
	tampered := append([]byte{}, bs...)
	if tampered[pos] == 'A' {
		tampered[pos] = 'B'
	} else {
		tampered[pos] = 'A'
	}

	_, err := manifestcodec.DecodeFileEncrypted(tampered, resolverWith("", dek))
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

// --- Envelope round-trip ---

func TestEnvelope_RoundTrip(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Namespace = "secret/ns"

	bs, err := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")
	if err != nil {
		t.Fatalf("EncodeFileEncrypted: %v", err)
	}

	got, err := manifestcodec.DecodeFileEncrypted(bs, resolverWith("", dek))
	if err != nil {
		t.Fatalf("DecodeFileEncrypted: %v", err)
	}
	if got.Namespace != src.Namespace {
		t.Errorf("Namespace round-trip: got %q, want %q", got.Namespace, src.Namespace)
	}
}

func TestEnvelope_SystemFieldsAreEncrypted(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Namespace = "tenant-a/secret"

	bs, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")

	// Envelope hides everything in body, including Namespace.
	if bytes.Contains(bs, []byte("tenant-a/secret")) {
		t.Error("Envelope leaked Namespace to plaintext")
	}
}

func TestEnvelope_NondeterministicArtifactID(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()

	a, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")
	b, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")
	if bytes.Equal(a, b) {
		t.Fatal("Envelope must produce different bytes per call (fresh IV)")
	}
}

// --- Plain forwarding ---

func TestDecodeFileEncrypted_PlainForwards(t *testing.T) {
	src := sampleManifest()
	bs, _ := manifestcodec.EncodeFile(
		src, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)

	// Plain manifest with nil resolver — must succeed.
	got, err := manifestcodec.DecodeFileEncrypted(bs, nil)
	if err != nil {
		t.Fatalf("DecodeFileEncrypted on Plain with nil resolver: %v", err)
	}
	if got.Namespace != src.Namespace {
		t.Errorf("Namespace: got %q, want %q", got.Namespace, src.Namespace)
	}
}

// --- Key rotation: multiple candidate keys ---

func TestDecodeFileEncrypted_RotationCandidates(t *testing.T) {
	oldDEK := freshDEK(t)
	newDEK := freshDEK(t)

	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, oldDEK, "")

	// Provide [newDEK, oldDEK] — the second one wins.
	resolver := &staticKeyProvider{byKeyID: map[string][][]byte{
		"": {newDEK, oldDEK},
	}}
	got, err := manifestcodec.DecodeFileEncrypted(bs, resolver)
	if err != nil {
		t.Fatalf("decode with rotation candidates: %v", err)
	}
	if got.Namespace == "" {
		t.Error("expected fully decoded manifest")
	}
}

// --- Refusal cases ---

func TestEncodeFileEncrypted_RejectsPlain(t *testing.T) {
	dek := freshDEK(t)
	_, err := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoPlain, dek, "")
	if err == nil {
		t.Fatal("Plain crypto must be rejected by EncodeFileEncrypted")
	}
}

func TestEncodeFileEncrypted_RejectsEmptyDEK(t *testing.T) {
	_, err := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, nil, "")
	if err == nil {
		t.Fatal("empty DEK must be rejected")
	}
}

func TestDecodeFileEncrypted_NilResolverOnEncrypted(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")

	_, err := manifestcodec.DecodeFileEncrypted(bs, nil)
	if !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeFileEncrypted_EmptyKeyList(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "default")

	// Resolver knows about a different KeyID; for "default" it
	// returns an empty slice.
	resolver := &staticKeyProvider{byKeyID: map[string][][]byte{
		"other": {dek},
	}}
	_, err := manifestcodec.DecodeFileEncrypted(bs, resolver)
	if !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeFileEncrypted_AllCandidatesWrong(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")

	wrongA := freshDEK(t)
	wrongB := freshDEK(t)
	resolver := &staticKeyProvider{byKeyID: map[string][][]byte{
		"": {wrongA, wrongB},
	}}
	_, err := manifestcodec.DecodeFileEncrypted(bs, resolver)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

// --- KeyID propagation ---

func TestEncodeFileEncrypted_KeyIDPropagatesToHeader(t *testing.T) {
	dek := freshDEK(t)
	bs, err := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "tenant-42")
	if err != nil {
		t.Fatal(err)
	}

	header, _, err := manifestcodec.ReadHeader(bs)
	if err != nil {
		t.Fatal(err)
	}
	if header.KeyID != "tenant-42" {
		t.Errorf("KeyID: got %q, want tenant-42", header.KeyID)
	}
}

// --- Sanity: encryption actually produces different bytes ---

func TestEncodeFileEncrypted_BodyDiffersFromPlain(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()

	plain, _ := manifestcodec.EncodeFile(
		src, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	envelope, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoEnvelope, dek, "")

	// Bodies (after header) must differ — Plain is JSON,
	// Envelope is ciphertext.
	if bytes.Equal(plain[5:], envelope[6:]) {
		t.Error("Envelope body bytes accidentally match Plain")
	}
}

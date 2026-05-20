package manifestcodec_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/manifestcodec"
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

// --- Sealed round-trip ---

func TestSealed_RoundTrip(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Ext = json.RawMessage(`{"fsmeta":{"path":"a.txt"}}`)
	src.Usr = json.RawMessage(`{"tenant":"acme","tags":["a","b"]}`)

	bs, err := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoSealed, dek, "")
	if err != nil {
		t.Fatalf("EncodeFileEncrypted: %v", err)
	}

	got, err := manifestcodec.DecodeFileEncrypted(bs, resolverWith("", dek))
	if err != nil {
		t.Fatalf("DecodeFileEncrypted: %v", err)
	}

	if !bytes.Equal([]byte(got.Ext), []byte(src.Ext)) {
		t.Errorf("ext round-trip: got %s, want %s",
			string(got.Ext), string(src.Ext))
	}
	if !bytes.Equal([]byte(got.Usr), []byte(src.Usr)) {
		t.Errorf("usr round-trip: got %s, want %s",
			string(got.Usr), string(src.Usr))
	}
	if got.Namespace != src.Namespace {
		t.Errorf("Namespace: got %q, want %q", got.Namespace, src.Namespace)
	}
	if got.BlobRef != src.BlobRef {
		t.Errorf("BlobRef: got %q, want %q", got.BlobRef, src.BlobRef)
	}
}

func TestSealed_SystemFieldsArePlaintext(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Namespace = "tenant-a/orders"
	src.Ext = json.RawMessage(`{"ext-secret":"do-not-leak-ext"}`)
	src.Usr = json.RawMessage(`{"usr-secret":"do-not-leak-usr"}`)

	bs, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoSealed, dek, "")

	// Without any decryption, body should still contain the
	// namespace string in plaintext (sys block stays open).
	if !bytes.Contains(bs, []byte("tenant-a/orders")) {
		t.Error("Sealed should leave Namespace in plaintext on disk")
	}
	// And the ext/usr contents must NOT be visible.
	if bytes.Contains(bs, []byte("do-not-leak-ext")) {
		t.Error("Sealed leaked ext to plaintext")
	}
	if bytes.Contains(bs, []byte("do-not-leak-usr")) {
		t.Error("Sealed leaked usr to plaintext")
	}
}

func TestSealed_EmptyExtAndUsr(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Ext = nil
	src.Usr = nil

	bs, err := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoSealed, dek, "")
	if err != nil {
		t.Fatalf("EncodeFileEncrypted: %v", err)
	}
	got, err := manifestcodec.DecodeFileEncrypted(bs, resolverWith("", dek))
	if err != nil {
		t.Fatalf("DecodeFileEncrypted: %v", err)
	}
	if len(got.Ext) != 0 {
		t.Errorf("ext: expected empty, got %s", string(got.Ext))
	}
	if len(got.Usr) != 0 {
		t.Errorf("usr: expected empty, got %s", string(got.Usr))
	}
}

func TestSealed_TamperedHeaderFailsDecryption(t *testing.T) {
	dek := freshDEK(t)
	// Sealed only seals ext/usr/inline_blob — sys stays in
	// plaintext. A manifest with all three blocks empty has no
	// ciphertext and therefore no AAD anchor that would catch
	// a header tamper at the codec layer (ArtifactID does that
	// at the core layer). The test sets Usr so there is a
	// sealed block whose AAD binds the header.
	src := sampleManifest()
	src.Usr = json.RawMessage(`{"tenant":"acme"}`)

	bs, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoSealed, dek, "tenant-a")

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

func TestSealed_TamperedCiphertext(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Usr = json.RawMessage(`{"tenant":"acme"}`)

	bs, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoSealed, dek, "")

	// Sealed stores each sealed block as base64 inside a JSON
	// string. Find the usr key (set above) and flip one
	// base64 character to break the AEAD tag.
	idx := bytes.Index(bs, []byte(`"usr":"`))
	if idx < 0 {
		t.Fatal("usr key not found at expected JSON position")
	}
	// First base64 character after the opening quote.
	pos := idx + len(`"usr":"`)
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

// --- Paranoid round-trip ---

func TestParanoid_RoundTrip(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Namespace = "secret/ns"

	bs, err := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "")
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

func TestParanoid_SystemFieldsAreEncrypted(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()
	src.Namespace = "tenant-a/secret"

	bs, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "")

	// Paranoid hides everything in body, including Namespace.
	if bytes.Contains(bs, []byte("tenant-a/secret")) {
		t.Error("Paranoid leaked Namespace to plaintext")
	}
}

func TestParanoid_NondeterministicArtifactID(t *testing.T) {
	dek := freshDEK(t)
	src := sampleManifest()

	a, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "")
	b, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "")
	if bytes.Equal(a, b) {
		t.Fatal("Paranoid must produce different bytes per call (fresh IV)")
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
		domain.ManifestCryptoParanoid, oldDEK, "")

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
		domain.ManifestCryptoParanoid, nil, "")
	if err == nil {
		t.Fatal("empty DEK must be rejected")
	}
}

func TestDecodeFileEncrypted_NilResolverOnEncrypted(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "")

	_, err := manifestcodec.DecodeFileEncrypted(bs, nil)
	if !errors.Is(err, errs.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestDecodeFileEncrypted_EmptyKeyList(t *testing.T) {
	dek := freshDEK(t)
	bs, _ := manifestcodec.EncodeFileEncrypted(
		sampleManifest(), domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "default")

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
		domain.ManifestCryptoParanoid, dek, "")

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
		domain.ManifestCryptoParanoid, dek, "tenant-42")
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
	Paranoid, _ := manifestcodec.EncodeFileEncrypted(
		src, domain.ManifestEncodingJSON,
		domain.ManifestCryptoParanoid, dek, "")

	// Bodies (after header) must differ — Plain is JSON,
	// Paranoid is ciphertext.
	if bytes.Equal(plain[5:], Paranoid[6:]) {
		t.Error("Paranoid body bytes accidentally match Plain")
	}
}

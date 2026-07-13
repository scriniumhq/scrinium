package artifact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
)

func TestSealed_CrossBlockSwapFails(t *testing.T) {
	m := artifactfx.Manifest(func(m *domain.Manifest) {
		// Equal length for a clean base64 swap
		m.Ext = json.RawMessage(`{"a":"ext-data-AAAA"}`)
		m.Usr = json.RawMessage(`{"a":"usr-data-BBBB"}`)
	})

	_, bs := artifactfx.Encoded(t, m, config.ManifestCryptoSealed)

	extKey := []byte(`"ext":"`)
	usrKey := []byte(`"usr":"`)
	extStart := bytes.Index(bs, extKey)
	usrStart := bytes.Index(bs, usrKey)
	if extStart < 0 || usrStart < 0 {
		t.Fatalf("ext or usr field not found in body: %s", bs)
	}
	extCTStart := extStart + len(extKey)
	usrCTStart := usrStart + len(usrKey)
	extCTEnd := extCTStart + bytes.IndexByte(bs[extCTStart:], '"')
	usrCTEnd := usrCTStart + bytes.IndexByte(bs[usrCTStart:], '"')

	extCT := slices.Clone(bs[extCTStart:extCTEnd])
	usrCT := slices.Clone(bs[usrCTStart:usrCTEnd])

	tampered := slices.Clone(bs)
	copy(tampered[extCTStart:extCTEnd], usrCT)
	copy(tampered[usrCTStart:usrCTEnd], extCT)

	_, err := artifact.DecodeEncrypted(tampered, artifactfx.Keys())
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed on cross-block swap, got %v", err)
	}
}

func TestSealed_TamperedHeaderFailsDecryption(t *testing.T) {
	// AAD is bound to the header. Mutating the KeyID in the header must break parsing of Sealed blocks.
	_, bs := artifactfx.Encoded(t, artifactfx.Manifest(), config.ManifestCryptoSealed)

	idx := bytes.Index(bs, []byte("k1"))
	if idx < 0 {
		t.Fatal("test setup: KeyID 'k1' not found in header")
	}
	tampered := slices.Clone(bs)
	tampered[idx] = 'x' // k1 -> x1

	// KeyID changed; pass a provider that knows "x1"
	_, err := artifact.DecodeEncrypted(tampered, artifactfx.Keys())
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed (header AAD mismatch), got %v", err)
	}
}

func TestSealed_TamperedCiphertext(t *testing.T) {
	_, bs := artifactfx.Encoded(t, artifactfx.Manifest(), config.ManifestCryptoSealed)

	idx := bytes.Index(bs, []byte(`"usr":"`))
	pos := idx + len(`"usr":"`)
	tampered := slices.Clone(bs)
	if tampered[pos] == 'A' {
		tampered[pos] = 'B'
	} else {
		tampered[pos] = 'A'
	}

	_, err := artifact.DecodeEncrypted(tampered, artifactfx.Keys())
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestParanoid_NondeterministicArtifactID(t *testing.T) {
	m := artifactfx.Manifest()
	id1, _ := artifactfx.Encoded(t, m, config.ManifestCryptoParanoid)
	id2, _ := artifactfx.Encoded(t, m, config.ManifestCryptoParanoid)
	if id1 == id2 {
		t.Fatal("Paranoid must produce different ArtifactID per call (fresh IV)")
	}
}

func TestDecodeEncrypted_RotationCandidates(t *testing.T) {
	oldDEK := artifactfx.DEK()
	newDEK := bytes.Repeat([]byte{0x99}, 32)

	// Manifest encrypted with the old key
	_, bs := artifactfx.Encoded(t, artifactfx.Manifest(), config.ManifestCryptoParanoid)

	// Provider returns the key array [new, old]
	provider := artifactfx.Keys(newDEK, oldDEK)

	_, err := artifact.DecodeEncrypted(bs, provider)
	if err != nil {
		t.Fatalf("decode with rotation candidates: %v", err)
	}
}

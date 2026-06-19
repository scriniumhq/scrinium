package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// This file covers the on-disk integrity invariant: the ArtifactID is
// the hash of the WHOLE manifest file (header included), so any byte
// flipped on disk is caught at read time before the codec runs. It is
// an external (package store_test) test, anchored to engine/store only
// because it inspects and rewrites raw driver bytes through the
// test-only StoreKeyResolver / ReadDriverFile / WriteDriverFile bridge
// in export_test.go — there is no public surface for that.

// fixedKeyIDResolver is a test-only KeyResolver that hands the same DEK
// out for every KeyID and reports a non-empty KeyID from
// ResolveWriteKey, so the manifest header carries KeyID bytes the test
// can tamper with.
type fixedKeyIDResolver struct {
	keyID string
	dek   []byte
}

func (r *fixedKeyIDResolver) GetKeys(_ string) ([][]byte, error) {
	return [][]byte{append([]byte{}, r.dek...)}, nil
}
func (r *fixedKeyIDResolver) ResolveWriteKey(pipeline.KeyContext) string { return r.keyID }

// payloadReader is a minimal Put helper: returns an Artifact backed by a
// byte reader plus the original bytes for downstream comparison.
func payloadReader(s string) (a domain.Artifact, raw []byte) {
	raw = []byte(s)
	a = domain.Artifact{
		Payload: bytes.NewReader(raw),
		Usr:     json.RawMessage(`{"tag":"x"}`),
	}
	return
}

// manifestPathFor returns the on-disk path of an artifact's manifest.
// Manifests are sharded by ManifestDigest (not by the floating handle),
// so resolve the digest and replicate the manifests/<x>/<y>/<digest>
// layout that blobpath.ManifestPath builds.
func manifestPathFor(t *testing.T, s store.Store, id domain.ArtifactID) string {
	t.Helper()
	dStr := string(storekit.MustDigest(t, s, id))
	if len(dStr) < 4 {
		t.Fatal("digest too short")
	}
	return "manifests/" + dStr[:2] + "/" + dStr[2:4] + "/" + dStr
}

// TestGet_TamperedKeyIDInHeader_ReturnsCorruptedManifest verifies the
// §3.4 invariant: ArtifactID = hash(file bytes, header included).
// Tampering with the KeyID without touching ciphertext changes the file
// hash and therefore the ArtifactID; loadManifest catches the mismatch
// BEFORE attempting decryption.
//
// Distinct from the AAD path inside the codec (TamperedHeaderFails-
// Decryption): this test stops earlier — at VerifyArtifactID — and
// confirms the stronger "ArtifactID locks the file as a whole"
// invariant.
func TestGet_TamperedKeyIDInHeader_ReturnsCorruptedManifest(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoParanoid}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))

	// AutoUnlock so the engine holds a DEK, then read that DEK back
	// through the auto-promoted resolver: the custom resolver below has
	// to wrap under the SAME key the engine unwrapped, or the ciphertext
	// would be undecryptable for reasons unrelated to the tamper.
	autoOpened := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)
	auto := store.StoreKeyResolver(autoOpened)
	keys, err := auto.GetKeys("")
	if err != nil || len(keys) == 0 {
		t.Fatalf("auto resolver: %v / %d keys", err, len(keys))
	}
	dek := keys[0]

	// Reopen with a custom resolver that reuses that DEK but publishes
	// "tenant-X" via ResolveWriteKey, so Put writes that KeyID into the
	// file header. A FRESH index makes this a separate session —
	// otherwise the previous Open's auto-promoted resolver would take
	// precedence.
	fresh := indexfx.Memory(t)
	custom := &fixedKeyIDResolver{keyID: "tenant-X", dek: dek}
	s, err := store.OpenStore(context.Background(), r.Driver(),
		store.WithConfig(cfg),
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithKeyResolver(custom),
		store.WithStoreIndex(fresh),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := payloadReader("payload")
	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read the raw manifest, flip one byte of the KeyID, write it back.
	// The KeyID starts at byte 6 (magic 4 + flag 1 + length 1).
	manifestPath := manifestPathFor(t, s, id)
	raw, err := store.ReadDriverFile(s, manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(raw) < 14 {
		t.Fatalf("manifest too short: %d bytes", len(raw))
	}
	tampered := append([]byte{}, raw...)
	if tampered[6] == 'X' {
		tampered[6] = 'Y'
	} else {
		tampered[6] = 'X'
	}

	if err := store.WriteDriverFile(s, manifestPath, tampered); err != nil {
		t.Fatalf("write tampered manifest: %v", err)
	}

	// Get must surface ErrCorruptedManifest at VerifyArtifactID, before
	// the codec ever tries to Open the body.
	_, err = s.Get(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Fatalf("expected ErrCorruptedManifest, got %v", err)
	}
}

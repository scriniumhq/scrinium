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
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// --- KeyResolver promotion ---

func TestKeyResolverPromotion_OnUnlock(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Open without AutoUnlock — Locked, no DEK yet.
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) != nil {
		t.Error("Locked Store should have no KeyResolver yet")
	}
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("Unlock should populate the default KeyResolver")
	}
}

func TestKeyResolverPromotion_OnAutoUnlock(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("AutoUnlock should populate default KeyResolver")
	}
}

func TestKeyResolverPromotion_RespectsCustomResolver(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	customDEK := bytes.Repeat([]byte{0xAB}, 32)
	custom := pipeline.NewStaticKeyResolver(customDEK)

	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithKeyResolver(custom),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := store.StoreKeyResolver(s)
	if got == nil {
		t.Fatal("KeyResolver should not be nil")
	}
	// The custom resolver MUST have survived AutoUnlock — verify
	// by querying it (it returns customDEK, not s.dek).
	keys, err := got.GetKeys("")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], customDEK) {
		t.Error("AutoUnlock overwrote the caller's custom KeyResolver")
	}
}

func TestKeyResolverPromotion_OnInitStore(t *testing.T) {
	drv := driverfx.LocalFS(t)
	s, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("InitStore on encrypted Store should populate default KeyResolver")
	}
}

// --- Tampered KeyID surfaces ErrCorruptedManifest at Get ---

// fixedKeyIDResolver is a test-only KeyResolver that hands the
// same DEK out for every KeyID and returns a non-empty
// KeyID from ResolveWriteKey so the manifest header carries KeyID
// bytes we can tamper with.
type fixedKeyIDResolver struct {
	keyID string
	dek   []byte
}

func (r *fixedKeyIDResolver) GetKeys(_ string) ([][]byte, error) {
	return [][]byte{append([]byte{}, r.dek...)}, nil
}
func (r *fixedKeyIDResolver) ResolveWriteKey(pipeline.KeyContext) string { return r.keyID }

// TestGet_TamperedKeyIDInHeader_ReturnsCorruptedManifest verifies
// the §3.4 invariant: ArtifactID = hash(file bytes including
// header). Tampering with the KeyID without touching ciphertext
// changes the file hash and therefore the ArtifactID;
// loadManifest catches the mismatch BEFORE attempting decryption.
//
// Distinct from TestSealed_TamperedHeaderFailsDecryption,
// which exercises the AAD path inside the codec. This test
// stops earlier — at VerifyArtifactID — and confirms the
// stronger "ArtifactID locks the file as a whole" invariant.
func TestGet_TamperedKeyIDInHeader_ReturnsCorruptedManifest(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoParanoid}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))

	// AutoUnlock so the engine has a DEK; then we override the
	// auto-promoted resolver with one whose ResolveWriteKey is
	// non-empty so the file header carries a KeyID we can
	// tamper with. The DEK has to match what the engine
	// unwrapped — we read it indirectly through the resolver
	// the auto-promotion installed.
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

	// Reopen with a custom resolver that uses the same DEK but
	// publishes "tenant-X" via ResolveWriteKey, so Put writes that
	// KeyID into the file header. A FRESH index is required so
	// the engine treats this as a separate session — the auto-
	// promoted resolver from the previous Open would otherwise
	// take precedence.
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

	// Read raw manifest from disk, tamper one byte of the KeyID,
	// write it back. The KeyID starts at byte 6 (magic 4 + flag 1
	// + length 1).
	manifestPath := manifestPathFor(t, s, id)
	raw, err := store.ReadDriverFile(s, manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(raw) < 14 {
		t.Fatalf("manifest too short: %d bytes", len(raw))
	}
	tampered := append([]byte{}, raw...)
	// Byte 6 = first byte of KeyID. Flip it to anything else.
	if tampered[6] == 'X' {
		tampered[6] = 'Y'
	} else {
		tampered[6] = 'X'
	}

	if err := store.WriteDriverFile(s, manifestPath, tampered); err != nil {
		t.Fatalf("write tampered manifest: %v", err)
	}

	// Get must surface ErrCorruptedManifest at VerifyArtifactID,
	// before the codec ever tries to Open the body.
	_, err = s.Get(context.Background(), id)
	if !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Fatalf("expected ErrCorruptedManifest, got %v", err)
	}
}

func manifestPathFor(t *testing.T, s store.Store, id domain.ArtifactID) string {
	t.Helper()
	// Manifest files are named by their ManifestDigest, not the handle.
	dStr := string(storekit.MustDigest(t, s, id))
	if len(dStr) < 4 {
		t.Fatal("digest too short")
	}
	return "manifests/" + dStr[:2] + "/" + dStr[2:4] + "/" + dStr
}

// payloadReader is a minimal helper for Put: returns a byte
// reader and the original bytes for downstream comparison.
func payloadReader(s string) (a domain.Artifact, raw []byte) {
	raw = []byte(s)
	a = domain.Artifact{
		Payload: bytes.NewReader(raw),
		Usr:     json.RawMessage(`{"tag":"x"}`),
	}
	return
}

// --- Sealed: usr metadata stays confidential on disk ---

// initEncryptedWithCrypto bootstraps an encrypted Store with the
// requested ManifestCrypto and reopens it with AutoUnlock so
// the returned Store is ready to Put. The same WithConfig is
// passed to Init and Open — otherwise OpenStore reports a config
// mismatch against the persisted system.config artifact.
func initEncryptedWithCrypto(t *testing.T, crypto domain.ManifestCrypto) store.Store {
	t.Helper()
	cfg := domain.StoreConfig{ManifestCrypto: crypto}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))
	return r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)
}

func TestPut_Sealed_UsrMetadataConfidential(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoSealed)
	a, _ := payloadReader("payload")
	a.Usr = json.RawMessage(`{"secret":"do-not-leak"}`)

	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read raw manifest file from disk: usr metadata must not be plaintext.
	bytesOnDisk := readManifestRaw(t, s, id)
	if bytes.Contains(bytesOnDisk, []byte("do-not-leak")) {
		t.Error("Sealed leaked usr metadata to plaintext")
	}
}

// --- Helper: read raw manifest file via Driver, bypassing Get ---

func readManifestRaw(t *testing.T, s store.Store, id domain.ArtifactID) []byte {
	t.Helper()
	// The manifest file is named by its ManifestDigest, not by the floating
	// handle. Resolve the digest, then replicate the shard layout
	// (manifests/<x>/<y>/<digest>) the way blobpath.ManifestPath does.
	digest := storekit.MustDigest(t, s, id)
	dStr := string(digest)
	if len(dStr) < 4 {
		t.Fatal("digest too short")
	}
	path := "manifests/" + dStr[:2] + "/" + dStr[2:4] + "/" + dStr

	raw, err := store.ReadDriverFile(s, path)
	if err != nil {
		t.Fatalf("read manifest from disk: %v", err)
	}
	return raw
}

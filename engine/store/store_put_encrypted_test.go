package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/pipeline/stage/aesgcm"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	storefx "scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

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

// --- Put on Plain Store still works ---

func TestPut_PlainStillWorks(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	a, _ := payloadReader("plain payload")
	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("ArtifactID is empty")
	}
}

// --- Put with Sealed ---

func TestPut_Sealed_Succeeds(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoSealed)
	a, _ := payloadReader("sealed payload")
	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("ArtifactID is empty")
	}
}

// --- Put with Paranoid ---

func TestPut_Paranoid_Succeeds(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoParanoid)
	a, _ := payloadReader("Paranoid payload")
	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("ArtifactID is empty")
	}
}

// --- Put on encrypted Store while Locked ---

func TestPut_EncryptedManifestRejectedWhenLocked(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoParanoid}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))
	// Open WITHOUT AutoUnlock: Store is in StateLocked.
	s := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithConfig(cfg),
	)

	a, _ := payloadReader("payload")
	_, err := s.Put(context.Background(), a)
	if !errors.Is(err, errs.ErrLocked) {
		t.Fatalf("expected ErrLocked on Put while Locked, got %v", err)
	}
}

// --- Sealed: usr metadata stays confidential on disk ---

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

// --- End-to-end Put → Get ---

func TestPutGet_Sealed_RoundTrip(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoSealed)
	a, raw := payloadReader("sealed end-to-end")
	a.Usr = json.RawMessage(`{"tag":"value"}`)

	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("payload round-trip: got %q, want %q", got, raw)
	}
}

func TestPutGet_Paranoid_RoundTrip(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoParanoid)
	a, raw := payloadReader("Paranoid end-to-end")

	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("payload round-trip: got %q, want %q", got, raw)
	}
}

// TestGet_LockedRejectsEncryptedManifest verifies that a Locked
// Store cannot read an encrypted manifest: the codec asks for
// keys, the resolver is nil, and ErrKeyNotFound surfaces.
func TestGet_LockedRejectsEncryptedManifest(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoParanoid}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))

	// Open with AutoUnlock first to write a manifest.
	s := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)
	a, _ := payloadReader("payload")
	id, err := s.Put(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}

	// Reopen WITHOUT AutoUnlock — Locked.
	locked := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithConfig(cfg),
	)

	// Get on Locked Store is blocked at checkOperational
	// (StateLocked → ErrLocked) before reaching the manifest.
	// We do NOT reach the codec layer here; that path is for
	// future scenarios where a Store is Plain-DEK but has
	// encrypted manifests via a custom KeyResolver.
	_, err = locked.Get(context.Background(), id)
	if !errors.Is(err, errs.ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

// --- Paranoid blob dedup invariant ---

// TestPut_EncryptedBlobsDoNotDedup is the regression test for the
// M2 data-loss bug ADR-58 closes. The store encrypts BLOBS via a
// crypto Pipeline stage (aes-gcm) — that is what gives a blob a
// non-empty crypto-identity; ManifestCrypto governs the manifest
// body, a separate axis (Encryption Model §5.4).
//
// With EncryptedDedup defaulting to Disabled, N writes of the SAME
// plaintext produce N distinct ArtifactIDs AND N distinct blobs on
// disk (random IV -> distinct ciphertext -> distinct BlobRef).
// Crucially every manifest stays independently readable — the old
// behaviour collapsed them onto one blob whose IV matched only the
// first writer, so the 2nd and 3rd Get failed with
// ErrDecryptionFailed.
//
// The pinned-DEK aesgcm factory records an empty KeyID, so the
// crypto-identity here is "aes-gcm/" — non-empty, which is all the
// dedup probe needs to take the encrypted branch.
func TestPut_EncryptedBlobsDoNotDedup(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, err := aesgcm.New(dek)
	if err != nil {
		t.Fatalf("aesgcm.New: %v", err)
	}
	reg := pipeline.NewTransformerRegistry().Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{Pipeline: []string{"aes-gcm"}}
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	s := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	const samePayload = "encrypted no-dedup payload"
	ids := make([]domain.ArtifactID, 0, 3)
	for i := 0; i < 3; i++ {
		a, _ := payloadReader(samePayload)
		id, err := s.Put(context.Background(), a)
		if err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// (a) Three distinct ArtifactIDs — ciphertext non-determinism.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				t.Fatalf("distinct ArtifactIDs expected, identical at %d/%d: %s", i, j, ids[i])
			}
		}
	}

	// (b) Three blobs on disk — encrypted blobs do NOT dedup under
	// Disabled. This is the assertion that read "1" before ADR-58.
	disk := storefx.OnDiskAt(drv.Root())
	if blobCount := disk.BlobCount(); blobCount != 3 {
		t.Errorf("Disabled: 3 Puts of same plaintext should yield 3 blobs, got %d", blobCount)
	}

	// (c) Every manifest is independently readable — the property
	// the bug violated. Each blob decrypts under its own IV.
	for i, id := range ids {
		rh, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get id[%d]: %v", i, err)
		}
		got, _ := io.ReadAll(rh)
		_ = rh.Close()
		if string(got) != samePayload {
			t.Errorf("id[%d] payload: got %q, want %q", i, got, samePayload)
		}
	}

	// (d) Deleting one leaves the others intact (independent blobs,
	// no shared ref_count to confuse).
	if err := s.Delete(context.Background(), ids[0]); err != nil {
		t.Fatalf("Delete[0]: %v", err)
	}
	rh, err := s.Get(context.Background(), ids[1])
	if err != nil {
		t.Fatalf("Get id[1] after Delete[0]: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if string(got) != samePayload {
		t.Errorf("survivor payload: got %q, want %q", got, samePayload)
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

// --- Walk on Paranoid Store works without decrypting ---

// alwaysFailingResolver returns errors for any GetKeys call.
// Used to prove that a code path does NOT consult the resolver.
type alwaysFailingResolver struct{}

func (alwaysFailingResolver) GetKeys(_ string) ([][]byte, error) {
	return nil, errors.New("alwaysFailingResolver: should not be called")
}
func (alwaysFailingResolver) ResolveWriteKey(pipeline.KeyContext) string { return "" }

// TestWalk_ParanoidStoreWalksWithoutDecryption verifies the §3.5
// invariant: in Paranoid mode, Namespace is encrypted inside the
// manifest file but stored in plaintext in StoreIndex. Walk
// therefore returns matches by querying the index alone, never
// reading or decrypting manifest bodies.
//
// We prove this by Put'ing some artifacts under a real resolver,
// then reopening with a sabotaged resolver that errors on every
// GetKeys call. Walk must still succeed and return the expected
// row count.
func TestWalk_ParanoidStoreWalksWithoutDecryption(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoParanoid}
	_, r := storefx.InitEncrypted(t, "pw", store.WithConfig(cfg))

	// Phase 1: Put with the auto-promoted resolver.
	s1 := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithConfig(cfg),
	)
	const n = 5
	for i := 0; i < n; i++ {
		a, _ := payloadReader(fmt.Sprintf("Paranoid payload %d", i))
		if _, err := s1.Put(context.Background(), a); err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
	}

	// Phase 2: reopen with a resolver that errors on every call,
	// reusing the same Driver and the SAME idx (Reopener carries
	// both).
	//
	// Note on cold-start: a freshly reopened Store with an empty
	// index would normally trip the Orphan Scan into deleting
	// all manifests (known gap, backlog §3.1). For this test we
	// reuse the original idx so the manifests stay in place —
	// that is exactly what r.Open guarantees.
	s2 := r.Open(t,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithKeyResolver(alwaysFailingResolver{}),
		store.WithConfig(cfg),
	)

	// Walk must succeed without ever consulting the resolver.
	count := 0
	if err := s2.Walk(context.Background(), func(domain.Manifest) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("Walk on Paranoid Store with broken resolver: %v\n"+
			"Walk must NOT decrypt manifest bodies — namespace lookup is index-only", err)
	}
	if count != n {
		t.Errorf("Walk row count: got %d, want %d", count, n)
	}
}

package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/testutil/storefx"
	"scrinium.dev/internal/testutil/indexfx"
)

// initEncryptedWithCrypto bootstraps an encrypted Store with the
// requested ManifestCrypto and reopens it with AutoUnlock so
// the returned Store is ready to Put. The same WithConfig is
// passed to Init and Open — otherwise OpenStore reports a config
// mismatch against the persisted system.config artifact.
func initEncryptedWithCrypto(t *testing.T, crypto domain.ManifestCrypto) core.Store {
	t.Helper()
	cfg := domain.StoreConfig{ManifestCrypto: crypto}
	_, r := storefx.InitEncrypted(t, "pw", core.WithConfig(cfg))
	return r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithConfig(cfg),
	)
}

// payloadReader is a minimal helper for Put: returns a byte
// reader and the original bytes for downstream comparison.
func payloadReader(s string) (a domain.Artifact, raw []byte) {
	raw = []byte(s)
	a = domain.Artifact{
		Payload:  bytes.NewReader(raw),
		Metadata: json.RawMessage(`{"tag":"x"}`),
	}
	return
}

// --- Put on Plain Store still works ---

func TestPut_PlainStillWorks(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	a, _ := payloadReader("plain payload")
	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("ArtifactID is empty")
	}
}

// --- Put with MetadataOnly ---

func TestPut_MetadataOnly_Succeeds(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoMetadataOnly)
	a, _ := payloadReader("metadata-only payload")
	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("ArtifactID is empty")
	}
}

// --- Put with Envelope ---

func TestPut_Envelope_Succeeds(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoEnvelope)
	a, _ := payloadReader("envelope payload")
	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("ArtifactID is empty")
	}
}

// --- Put on encrypted Store while Locked ---

func TestPut_EncryptedManifestRejectedWhenLocked(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoEnvelope}
	_, r := storefx.InitEncrypted(t, "pw", core.WithConfig(cfg))
	// Open WITHOUT AutoUnlock: Store is in StateLocked.
	s := r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithConfig(cfg),
	)

	a, _ := payloadReader("payload")
	_, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if !errors.Is(err, errs.ErrLocked) {
		t.Fatalf("expected ErrLocked on Put while Locked, got %v", err)
	}
}

// --- MetadataOnly: system fields stay in plaintext on disk ---

func TestPut_MetadataOnly_NamespaceVisibleOnDisk(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoMetadataOnly)
	a, _ := payloadReader("payload")
	a.Metadata = json.RawMessage(`{"secret":"do-not-leak"}`)

	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "tenant-a"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read raw manifest file from disk and check field visibility.
	bytesOnDisk := readManifestRaw(t, s, id)
	if !bytes.Contains(bytesOnDisk, []byte("tenant-a")) {
		t.Error("MetadataOnly should leave Namespace in plaintext on disk")
	}
	if bytes.Contains(bytesOnDisk, []byte("do-not-leak")) {
		t.Error("MetadataOnly leaked metadata to plaintext")
	}
}

// --- Envelope: even Namespace is hidden ---

func TestPut_Envelope_NamespaceHiddenOnDisk(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoEnvelope)
	a, _ := payloadReader("payload")

	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "tenant-secret"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	bytesOnDisk := readManifestRaw(t, s, id)
	if bytes.Contains(bytesOnDisk, []byte("tenant-secret")) {
		t.Error("Envelope leaked Namespace to plaintext on disk")
	}
}

// --- Helper: read raw manifest file via Driver, bypassing Get ---

func readManifestRaw(t *testing.T, s core.Store, id domain.ArtifactID) []byte {
	t.Helper()
	// Reach into the Store's Driver. core/export_test.go has no
	// helper for this; use Walk to find the index entry, then
	// read via testutil.
	//
	// Simpler: each test creates its own Driver via driverfx, and
	// the path is well-known. Compute it from id and read directly.
	// blobpath.ManifestPath produces a "manifests/<x>/<y>/<id>"
	// pattern; we replicate it minimally by string ops since this
	// is a test-only helper.
	idStr := string(id)
	if !strings.HasPrefix(idStr, "sha256-") {
		t.Fatalf("unexpected id prefix: %q", idStr)
	}
	hex := strings.TrimPrefix(idStr, "sha256-")
	if len(hex) < 4 {
		t.Fatal("id too short")
	}
	path := "manifests/" + hex[:2] + "/" + hex[2:4] + "/" + idStr

	raw, err := core.ReadDriverFile(s, path)
	if err != nil {
		t.Fatalf("read manifest from disk: %v", err)
	}
	return raw
}

// --- End-to-end Put → Get ---

func TestPutGet_MetadataOnly_RoundTrip(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoMetadataOnly)
	a, raw := payloadReader("metadata-only end-to-end")
	a.Metadata = json.RawMessage(`{"tag":"value"}`)

	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
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

func TestPutGet_Envelope_RoundTrip(t *testing.T) {
	s := initEncryptedWithCrypto(t, domain.ManifestCryptoEnvelope)
	a, raw := payloadReader("envelope end-to-end")

	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "secret"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
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
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoEnvelope}
	_, r := storefx.InitEncrypted(t, "pw", core.WithConfig(cfg))

	// Open with AutoUnlock first to write a manifest.
	s := r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithConfig(cfg),
	)
	a, _ := payloadReader("payload")
	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if err != nil {
		t.Fatal(err)
	}

	// Reopen WITHOUT AutoUnlock — Locked.
	locked := r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithConfig(cfg),
	)

	// Get on Locked Store is blocked at checkOperational
	// (StateLocked → ErrLocked) before reaching the manifest.
	// We do NOT reach the codec layer here; that path is for
	// future scenarios where a Store is Plain-DEK but has
	// encrypted manifests via a custom KeyResolver.
	_, err = locked.Get(context.Background(), id, domain.GetOptions{})
	if !errors.Is(err, errs.ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

// --- Envelope blob dedup invariant ---

// TestPutEnvelope_BlobDedupAcrossManifests verifies the §3.3
// invariant: in Envelope mode N writes of the same payload
// produce N distinct ArtifactIDs (manifest non-determinism)
// but exactly ONE blob with ref_count = N.
//
// Direct ref_count introspection isn't on the public API; we
// verify indirectly through the same pattern as
// TestDelete_SharedBlobKeepsRefCount: count blob files on disk,
// delete in order, observe that the blob survives until the
// last referrer.
func TestPutEnvelope_BlobDedupAcrossManifests(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoEnvelope}
	_, r := storefx.InitEncrypted(t, "pw", core.WithConfig(cfg))
	root := r.Root()

	s := r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithConfig(cfg),
	)

	const samePayload = "envelope dedup payload"
	ids := make([]domain.ArtifactID, 0, 3)
	for i := 0; i < 3; i++ {
		a, _ := payloadReader(samePayload)
		id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "ns"})
		if err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// (a) Three distinct ArtifactIDs — manifest non-determinism.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				t.Fatalf("Envelope must produce distinct ArtifactIDs per Put, got identical at %d/%d: %s",
					i, j, ids[i])
			}
		}
	}

	// (b) Exactly one blob file on disk — blob-level dedup.
	disk := storefx.OnDiskAt(root)
	if blobCount := disk.BlobCount(); blobCount != 1 {
		t.Errorf("after 3 Puts of the same payload, blobs/ should have 1 file, got %d", blobCount)
	}

	// (c) Delete two of three; blob must survive.
	if err := s.Delete(context.Background(), ids[0]); err != nil {
		t.Fatalf("Delete[0]: %v", err)
	}
	if err := s.Delete(context.Background(), ids[1]); err != nil {
		t.Fatalf("Delete[1]: %v", err)
	}
	if blobCount := disk.BlobCount(); blobCount != 1 {
		t.Errorf("blob should survive while one referrer remains, got %d files", blobCount)
	}
	// The third manifest is still readable — proves the blob
	// is intact, not just present.
	rh, err := s.Get(context.Background(), ids[2], domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get last surviving id: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if string(got) != samePayload {
		t.Errorf("payload after two deletes: got %q, want %q", got, samePayload)
	}

	// (d) Delete the last referrer. Per docs §5.3 (Asynchronous
	// Engine), Delete is logical only — the blob stays on disk
	// with ref_count == 0 until GC Agent (TODO M3.2) reaps it.
	// What we CAN verify here: every manifest is gone, and the
	// blob file is still present (waiting for GC).
	if err := s.Delete(context.Background(), ids[2]); err != nil {
		t.Fatalf("Delete[2]: %v", err)
	}
	for i, id := range ids {
		if _, err := s.Get(context.Background(), id, domain.GetOptions{}); !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Errorf("Get(ids[%d]) after final Delete: expected ErrArtifactNotFound, got %v", i, err)
		}
	}
	if blobCount := disk.BlobCount(); blobCount != 1 {
		t.Errorf("blob should remain on disk after last Delete (GC reaps it later), got %d files",
			blobCount)
	}
}

// --- Tampered KeyID surfaces ErrCorruptedManifest at Get ---

// fixedKeyIDResolver is a test-only KeyResolver that hands the
// same DEK out for every KeyID and returns a non-empty
// DefaultKeyID so the manifest header carries KeyID bytes we can
// tamper with.
type fixedKeyIDResolver struct {
	keyID string
	dek   []byte
}

func (r *fixedKeyIDResolver) GetKeys(_ string) ([][]byte, error) {
	return [][]byte{append([]byte{}, r.dek...)}, nil
}
func (r *fixedKeyIDResolver) DefaultKeyID() string { return r.keyID }

// TestGet_TamperedKeyIDInHeader_ReturnsCorruptedManifest verifies
// the §3.4 invariant: ArtifactID = hash(file bytes including
// header). Tampering with the KeyID without touching ciphertext
// changes the file hash and therefore the ArtifactID;
// loadManifest catches the mismatch BEFORE attempting decryption.
//
// Distinct from TestMetadataOnly_TamperedHeaderFailsDecryption,
// which exercises the AAD path inside the codec. This test
// stops earlier — at VerifyArtifactID — and confirms the
// stronger "ArtifactID locks the file as a whole" invariant.
func TestGet_TamperedKeyIDInHeader_ReturnsCorruptedManifest(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoEnvelope}
	_, r := storefx.InitEncrypted(t, "pw", core.WithConfig(cfg))

	// AutoUnlock so the engine has a DEK; then we override the
	// auto-promoted resolver with one whose DefaultKeyID is
	// non-empty so the file header carries a KeyID we can
	// tamper with. The DEK has to match what the engine
	// unwrapped — we read it indirectly through the resolver
	// the auto-promotion installed.
	autoOpened := r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithConfig(cfg),
	)
	auto := core.StoreKeyResolver(autoOpened)
	keys, err := auto.GetKeys("")
	if err != nil || len(keys) == 0 {
		t.Fatalf("auto resolver: %v / %d keys", err, len(keys))
	}
	dek := keys[0]

	// Reopen with a custom resolver that uses the same DEK but
	// publishes "tenant-X" as DefaultKeyID, so Put writes that
	// KeyID into the file header. A FRESH index is required so
	// the engine treats this as a separate session — the auto-
	// promoted resolver from the previous Open would otherwise
	// take precedence.
	fresh := indexfx.Memory(t)
	custom := &fixedKeyIDResolver{keyID: "tenant-X", dek: dek}
	s, err := core.OpenStore(context.Background(), r.Driver(),
		core.WithConfig(cfg),
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithKeyResolver(custom),
		core.WithStoreIndex(fresh),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := payloadReader("payload")
	id, err := s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read raw manifest from disk, tamper one byte of the KeyID,
	// write it back. The KeyID starts at byte 6 (magic 4 + flag 1
	// + length 1).
	manifestPath := manifestPathFor(t, id)
	raw, err := core.ReadDriverFile(s, manifestPath)
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

	if err := core.WriteDriverFile(s, manifestPath, tampered); err != nil {
		t.Fatalf("write tampered manifest: %v", err)
	}

	// Get must surface ErrCorruptedManifest at VerifyArtifactID,
	// before the codec ever tries to Open the body.
	_, err = s.Get(context.Background(), id, domain.GetOptions{})
	if !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Fatalf("expected ErrCorruptedManifest, got %v", err)
	}
}

// manifestPathFor reproduces blobpath.ManifestPath for tests
// without exporting the production helper. The path layout
// is sharded: manifests/<x>/<y>/<id> where <x><y> is the first
// 4 hex characters of the id past the "sha256-" prefix.
func manifestPathFor(t *testing.T, id domain.ArtifactID) string {
	t.Helper()
	idStr := string(id)
	if !strings.HasPrefix(idStr, "sha256-") {
		t.Fatalf("unexpected id prefix: %q", idStr)
	}
	hex := strings.TrimPrefix(idStr, "sha256-")
	if len(hex) < 4 {
		t.Fatal("id too short")
	}
	return "manifests/" + hex[:2] + "/" + hex[2:4] + "/" + idStr
}

// --- Walk on Envelope Store works without decrypting ---

// alwaysFailingResolver returns errors for any GetKeys call.
// Used to prove that a code path does NOT consult the resolver.
type alwaysFailingResolver struct{}

func (alwaysFailingResolver) GetKeys(_ string) ([][]byte, error) {
	return nil, errors.New("alwaysFailingResolver: should not be called")
}
func (alwaysFailingResolver) DefaultKeyID() string { return "" }

// TestWalk_EnvelopeStoreWalksWithoutDecryption verifies the §3.5
// invariant: in Envelope mode, Namespace is encrypted inside the
// manifest file but stored in plaintext in StoreIndex. Walk
// therefore returns matches by querying the index alone, never
// reading or decrypting manifest bodies.
//
// We prove this by Put'ing some artifacts under a real resolver,
// then reopening with a sabotaged resolver that errors on every
// GetKeys call. Walk must still succeed and return the expected
// row count.
func TestWalk_EnvelopeStoreWalksWithoutDecryption(t *testing.T) {
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoEnvelope}
	_, r := storefx.InitEncrypted(t, "pw", core.WithConfig(cfg))

	// Phase 1: Put with the auto-promoted resolver.
	s1 := r.Open(t,
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithConfig(cfg),
	)
	const n = 5
	for i := 0; i < n; i++ {
		a, _ := payloadReader(fmt.Sprintf("envelope payload %d", i))
		if _, err := s1.Put(context.Background(), a,
			domain.PutOptions{Namespace: "ns"}); err != nil {
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
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithAutoUnlock(),
		core.WithKeyResolver(alwaysFailingResolver{}),
		core.WithConfig(cfg),
	)

	// Walk must succeed without ever consulting the resolver.
	count := 0
	if err := s2.Walk(context.Background(), "ns", func(domain.Manifest) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("Walk on Envelope Store with broken resolver: %v\n"+
			"Walk must NOT decrypt manifest bodies — namespace lookup is index-only", err)
	}
	if count != n {
		t.Errorf("Walk row count: got %d, want %d", count, n)
	}
}

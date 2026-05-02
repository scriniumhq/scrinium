package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
)

// initEncryptedWithCrypto opens an encrypted Store with the
// requested ManifestCrypto. The Store is fully unlocked and
// ready to Put.
func initEncryptedWithCrypto(t *testing.T, crypto domain.ManifestCrypto) core.Store {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	cfg := domain.StoreConfig{ManifestCrypto: crypto}

	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	s, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithPassphrase(staticPP("pw")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s
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
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	cfg := domain.StoreConfig{ManifestCrypto: domain.ManifestCryptoEnvelope}

	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Open WITHOUT AutoUnlock: Store is in StateLocked.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := payloadReader("payload")
	_, err = s.Put(context.Background(), a, domain.PutOptions{Namespace: "u"})
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

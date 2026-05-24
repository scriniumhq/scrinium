package store_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/pipeline/stage/aesgcm"
	scriniumzstd "scrinium.dev/engine/pipeline/stage/zstd"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
	"scrinium.dev/internal/testutil/storefx"
)

var (
	payload = storefx.Payload
)

// --- Deferred surfaces ---

func TestPut_BlobTypeOtherThanRegular_Deferred(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{BlobType: domain.BlobTypeChunk})
	if err == nil {
		t.Fatal("expected error on Chunk BlobType")
	}
	if !strings.Contains(err.Error(), "M3") {
		t.Errorf("error should reference M3: %v", err)
	}
}

// --- Long payload streaming ---

func TestPut_LargePayload(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	const N = 1 << 20 // 1 MiB
	data := bytes.Repeat([]byte{0xab}, N)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(data)},
		domain.PutOptions{Namespace: "big"})
	if err != nil {
		t.Fatalf("Put 1MiB: %v", err)
	}

	var seen domain.Manifest
	if err := s.Walk(context.Background(), "big", func(m domain.Manifest) error {
		seen = m
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen.ArtifactID != id {
		t.Errorf("walked manifest ID: got %q, want %q", seen.ArtifactID, id)
	}
	if seen.OriginalSize != int64(N) {
		t.Errorf("OriginalSize: got %d, want %d", seen.OriginalSize, N)
	}
}

// --- Misc smoke ---

func TestPut_DefaultNamespace(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("default ns"),
		domain.PutOptions{}) // empty Namespace = default
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	// Visible via Walk("") (default namespace).
	var seen int
	_ = s.Walk(context.Background(), "", func(m domain.Manifest) error {
		seen++
		return nil
	})
	if seen != 1 {
		t.Errorf("default ns walk: got %d, want 1", seen)
	}
}

// --- Inline blobs (M1.4 pack 3) ---
//
// Inline mode kicks in when StoreConfig.BlobStorage is
// InlineFallback AND the payload size is at most InlineBlobLimit.
// The payload bytes are stored inside the manifest; no separate
// blob file appears under blobs/. Deduplication is disabled for
// inline manifests (their bytes have no row in the blobs table).

// helper: build a Store configured for InlineFallback. The limit
// is small enough that tests can exercise both sides of it
// cheaply.
func newInlineStore(t *testing.T, limit int64) (store.Store, string) {
	t.Helper()
	cfg := domain.StoreConfig{
		BlobStorage:     domain.BlobStorageInlineFallback,
		InlineBlobLimit: limit,
	}
	return storefx.InitWithRoot(t, store.WithConfig(cfg))
}

// --- Pipeline round-trip (M2.1) ---

func TestPut_Pipeline_ZstdRoundTrip(t *testing.T) {
	// Build a Store whose active config compresses via zstd. The
	// content we write must come back identical via Get.
	reg := pipeline.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))

	cfg := domain.StoreConfig{
		Pipeline: []string{"zstd"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	original := bytes.Repeat([]byte("scrinium "), 4096)
	id, err := store.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := store.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("round-trip mismatch (len got=%d want=%d)",
			len(got), len(original))
	}
	if rh.SupportsRandomAccess() {
		t.Fatalf("SupportsRandomAccess must be false for non-empty Pipeline")
	}
	if _, err := rh.ReadAt(make([]byte, 16), 0); !errors.Is(err, errs.ErrRandomAccessNotSupported) {
		t.Fatalf("ReadAt: got %v, want ErrRandomAccessNotSupported", err)
	}

	manifest := rh.Manifest()
	if len(manifest.Pipeline) != 1 || manifest.Pipeline[0].Algorithm != "zstd" {
		t.Fatalf("manifest Pipeline = %+v, want [{zstd}]", manifest.Pipeline)
	}
	if manifest.OriginalSize != int64(len(original)) {
		t.Fatalf("OriginalSize = %d, want %d", manifest.OriginalSize, len(original))
	}
}

func TestPut_Pipeline_AESGCMRoundTrip(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	aesFactory, err := aesgcm.New(dek)
	if err != nil {
		t.Fatalf("aesgcm.New: %v", err)
	}
	reg := pipeline.NewTransformerRegistry().Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{
		Pipeline: []string{"aes-gcm"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	original := []byte("Hello, ciphertext on disk")
	id, err := store.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := store.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("round-trip mismatch")
	}

	manifest := rh.Manifest()
	if len(manifest.Pipeline) != 1 || manifest.Pipeline[0].Algorithm != "aes-gcm" {
		t.Fatalf("manifest Pipeline = %+v", manifest.Pipeline)
	}
	// ADR-59: the segmented AEAD blob stores one IV per segment inside
	// the blob body, so the per-blob manifest stage IV is empty.
	if len(manifest.Pipeline[0].IV) != 0 {
		t.Fatalf("IV length = %d, want 0 (segmented format keeps IVs in frames)", len(manifest.Pipeline[0].IV))
	}
}

func TestPut_Pipeline_ZstdThenAESGCM(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i + 1)
	}
	aesFactory, _ := aesgcm.New(dek)
	reg := pipeline.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{})).
		Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{
		Pipeline: []string{"zstd", "aes-gcm"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	original := bytes.Repeat([]byte("compress then encrypt "), 1024)
	id, err := store.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(original)},
		domain.PutOptions{Namespace: "ns"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := store.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, _ := io.ReadAll(rh)
	if !bytes.Equal(got, original) {
		t.Fatalf("round-trip mismatch")
	}

	manifest := rh.Manifest()
	if len(manifest.Pipeline) != 2 ||
		manifest.Pipeline[0].Algorithm != "zstd" ||
		manifest.Pipeline[1].Algorithm != "aes-gcm" {
		t.Fatalf("manifest Pipeline = %+v", manifest.Pipeline)
	}
}

func TestPut_Pipeline_MissingAlgorithm(t *testing.T) {
	// Empty registry — "zstd" is not registered.
	reg := pipeline.NewTransformerRegistry()

	cfg := domain.StoreConfig{
		Pipeline: []string{"zstd"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store := storefx.InitOn(t, drv,
		store.WithStoreIndex(idx),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)

	_, err := store.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("x"))},
		domain.PutOptions{Namespace: "ns"})
	if !errors.Is(err, errs.ErrUnsupportedAlgorithm) {
		t.Fatalf("Put: got %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestPut_Pipeline_RefusedOnInline(t *testing.T) {
	reg := pipeline.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))

	cfg := domain.StoreConfig{
		Pipeline:        []string{"zstd"},
		BlobStorage:     domain.BlobStorageInlineFallback,
		InlineBlobLimit: 1024,
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store, _, err := store.InitStore(context.Background(), drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithReadRegistry(reg),
		store.WithConfig(cfg),
	)
	if err != nil {
		// If InitStore refuses Inline+Pipeline at config-validation
		// time (a future Rules Engine check), that is also a valid
		// outcome — the engine guarantees the combination is never
		// silently accepted.
		t.Skipf("InitStore refused Inline+Pipeline at startup: %v", err)
	}

	// Otherwise Put must refuse it explicitly.
	_, err = store.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("x"))},
		domain.PutOptions{Namespace: "ns"})
	if err == nil {
		t.Fatalf("Put: expected refusal for Inline + Pipeline, got nil")
	}
}

// --- Compile guard ---
var _ = io.Reader(strings.NewReader(""))

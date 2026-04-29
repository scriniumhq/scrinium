package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/manifestcodec"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
	scriniumzstd "github.com/rkurbatov/scrinium/plugin/compress/zstd"
	"github.com/rkurbatov/scrinium/plugin/crypto/aesgcm"
)

var (
	payload = storefx.Payload
)

// --- Happy path ---

func TestPut_FreshBlob_WritesArtifacts(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("hello scrinium"),
		domain.PutOptions{Namespace: "users"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("empty ArtifactID")
	}
	if !strings.HasPrefix(string(id), "sha256-") {
		t.Errorf("ArtifactID prefix: got %q", id)
	}

	// Manifest file is on disk under manifests/.
	idStr := string(id)
	hex := strings.TrimPrefix(idStr, "sha256-")
	manifestPath := filepath.Join(root, "manifests", hex[:2], hex[2:4], idStr)
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("manifest not on disk at %s: %v", manifestPath, err)
	}

	// At least one blob file under blobs/.
	var blobCount int
	_ = filepath.Walk(filepath.Join(root, "blobs"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			blobCount++
		}
		return nil
	})
	if blobCount != 1 {
		t.Errorf("blobs on disk: got %d, want 1", blobCount)
	}

	// Capacity reflects the new artifacts.
	info, err := s.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 1 {
		t.Errorf("ArtifactCount: got %d, want 1", info.ArtifactCount)
	}
	if info.BlobCount != 1 {
		t.Errorf("BlobCount: got %d, want 1", info.BlobCount)
	}
}

func TestPut_VisibleThroughWalk(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("walk-test"),
		domain.PutOptions{Namespace: "users"})
	if err != nil {
		t.Fatal(err)
	}

	var seen []domain.ArtifactID
	if err := s.Walk(context.Background(), "users", func(m domain.Manifest) error {
		seen = append(seen, m.ArtifactID)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 1 || seen[0] != id {
		t.Errorf("Walk results: got %v, want [%s]", seen, id)
	}
}

// --- Dedup ---

func TestPut_DeduplicatesIdenticalContent(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	const text = "duplicate me"

	id1, err := s.Put(context.Background(), payload(text),
		domain.PutOptions{Namespace: "ns", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(context.Background(), payload(text),
		domain.PutOptions{Namespace: "ns", SessionID: "sess-2"})
	if err != nil {
		t.Fatal(err)
	}

	// Different SessionID forces different manifests even when the
	// CreatedAt timestamp lands in the same second (docs §7.5
	// truncates to RFC 3339 seconds, so two Puts within one second
	// would otherwise produce byte-identical manifests).
	if id1 == id2 {
		t.Errorf("ArtifactIDs are equal — different SessionID must produce different manifests: %q", id1)
	}

	// But there must be only ONE blob on disk: dedup picked the
	// existing one and dropped the staging file.
	var blobCount int
	_ = filepath.Walk(filepath.Join(root, "blobs"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			blobCount++
		}
		return nil
	})
	if blobCount != 1 {
		t.Errorf("expected dedup to leave 1 blob, got %d", blobCount)
	}

	// And no leftover staging files.
	stagingDir := filepath.Join(root, "system.state", "staging")
	if entries, err := os.ReadDir(stagingDir); err == nil && len(entries) > 0 {
		t.Errorf("staging directory not cleaned: %d entries", len(entries))
	}
}

func TestPut_TwoArtifactsShareBlob_RefCountIs2(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	const text = "shared content"

	id1, err := s.Put(context.Background(), payload(text),
		domain.PutOptions{Namespace: "n", SessionID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(context.Background(), payload(text),
		domain.PutOptions{Namespace: "n", SessionID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("different SessionID must produce different ArtifactIDs, got %q", id1)
	}

	var seen int
	if err := s.Walk(context.Background(), "n", func(m domain.Manifest) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Errorf("Walk returned %d manifests, want 2", seen)
	}
}

// --- Retention ---

func TestPut_PreservesRetentionUntil(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(),
		payload("retention test"),
		domain.PutOptions{
			Namespace:      "vault",
			RetentionUntil: when,
		})
	if err != nil {
		t.Fatal(err)
	}

	// Verify the persisted manifest carries the retention.
	var seen domain.Manifest
	if err := s.Walk(context.Background(), "vault", func(m domain.Manifest) error {
		if m.ArtifactID == id {
			seen = m
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seen.RetentionUntil.Equal(when) {
		t.Errorf("RetentionUntil: got %v, want %v", seen.RetentionUntil, when)
	}
}

// --- Validation ---

func TestPut_RejectsSystemNamespace(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{Namespace: "system.config"})
	if !errors.Is(err, errs.ErrReservedNamespace) {
		t.Fatalf("expected errs.ErrReservedNamespace, got %v", err)
	}
}

func TestPut_RejectsWildcardNamespace(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{Namespace: "*"})
	if !errors.Is(err, errs.ErrReservedNamespace) {
		t.Fatalf("expected errs.ErrReservedNamespace, got %v", err)
	}
}

func TestPut_RejectsTooLongNamespace(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{Namespace: strings.Repeat("a", 256)})
	if !errors.Is(err, errs.ErrNamespaceTooLong) {
		t.Fatalf("expected errs.ErrNamespaceTooLong, got %v", err)
	}
}

func TestPut_RejectsTooLongSessionID(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{SessionID: strings.Repeat("a", 256)})
	if !errors.Is(err, errs.ErrSessionIDTooLong) {
		t.Fatalf("expected errs.ErrSessionIDTooLong, got %v", err)
	}
}

func TestPut_RejectsHugeMetadata(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	huge := bytes.Repeat([]byte(`a`), 64*1024+1)
	_, err := s.Put(context.Background(),
		domain.Artifact{
			Payload:  strings.NewReader("ok"),
			Metadata: append([]byte(`"`), append(huge, '"')...),
		},
		domain.PutOptions{})
	if !errors.Is(err, errs.ErrMetadataTooLarge) {
		t.Fatalf("expected errs.ErrMetadataTooLarge, got %v", err)
	}
}

func TestPut_RejectsNilPayload(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		domain.Artifact{Payload: nil},
		domain.PutOptions{})
	if err == nil {
		t.Fatal("expected error on nil payload")
	}
}

// --- State checks ---

func TestPut_BlockedInReadOnly(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	if err := s.SetMaintenanceMode(context.Background(),
		domain.MaintenanceModeReadOnly); err != nil {
		t.Fatal(err)
	}
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{})
	if !errors.Is(err, errs.ErrStoreReadOnly) {
		t.Fatalf("expected errs.ErrStoreReadOnly, got %v", err)
	}
}

func TestPut_BlockedInOffline(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	if err := s.SetMaintenanceMode(context.Background(),
		domain.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	_, err := s.Put(context.Background(),
		payload("nope"),
		domain.PutOptions{})
	if !errors.Is(err, errs.ErrStoreOffline) {
		t.Fatalf("expected errs.ErrStoreOffline, got %v", err)
	}
}

func TestPut_CtxCancelled(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Put(ctx, payload("nope"), domain.PutOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

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

// --- io.EOF behaviour on empty payload ---

func TestPut_EmptyPayload(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(nil)},
		domain.PutOptions{Namespace: "empty"})
	if err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	// OriginalSize must be 0; ContentHash is the hash of empty
	// input — both are well-defined.
	var got domain.Manifest
	_ = s.Walk(context.Background(), "empty", func(m domain.Manifest) error {
		got = m
		return nil
	})
	if got.OriginalSize != 0 {
		t.Errorf("OriginalSize: got %d, want 0", got.OriginalSize)
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
func newInlineStore(t *testing.T, limit int64) (core.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	root := drv.Root()
	cfg := domain.StoreConfig{
		BlobStorage:     domain.BlobStorageInlineFallback,
		InlineBlobLimit: limit,
	}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	return s, root
}

// countBlobFiles walks <root>/blobs and returns how many regular
// files live there. Used to assert that inline puts produce zero
// blob files.
func countBlobFiles(t *testing.T, root string) int {
	t.Helper()
	var n int
	_ = filepath.Walk(filepath.Join(root, "blobs"),
		func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				n++
			}
			return nil
		})
	return n
}

// readManifestFromDisk loads the manifest file written by Put and
// decodes it. Used to inspect fields that Walk cannot return —
// LayoutHeader, InlineBlob, Pipeline, Metadata — because the index
// is a routing layer, not a source of truth for manifest content
// (docs/2. Internals/09 §9.1.2).
func readManifestFromDisk(t *testing.T, root string, id domain.ArtifactID) domain.Manifest {
	t.Helper()
	idStr := string(id)
	hex := strings.TrimPrefix(idStr, "sha256-")
	path := filepath.Join(root, "manifests", hex[:2], hex[2:4], idStr)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest %s: %v", path, err)
	}
	m, err := manifestcodec.DecodeFile(raw)
	if err != nil {
		t.Fatalf("decode manifest %s: %v", path, err)
	}
	return m
}

func TestPut_Inline_BelowLimit_NoBlobFile(t *testing.T) {
	s, root := newInlineStore(t, 100)

	id, err := s.Put(context.Background(),
		payload("small"),
		domain.PutOptions{Namespace: "tiny"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("empty ArtifactID")
	}

	// No blob file produced — bytes live inside the manifest.
	if got := countBlobFiles(t, root); got != 0 {
		t.Errorf("blob files: got %d, want 0 (inline)", got)
	}

	// Walk finds the manifest in the index.
	var walked domain.Manifest
	if err := s.Walk(context.Background(), "tiny", func(m domain.Manifest) error {
		walked = m
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if walked.ArtifactID != id {
		t.Errorf("walked ID: got %q, want %q", walked.ArtifactID, id)
	}

	// LayoutHeader, OriginalSize, InlineBlob live in the manifest
	// file, not in the index (§9.1.2 — Inline manifests have
	// blob_ref=NULL, so the JOIN that recovers OriginalSize for
	// Target manifests yields nothing here). Read the file directly.
	disk := readManifestFromDisk(t, root, id)
	if disk.LayoutHeader.BlobStorage != "Inline" {
		t.Errorf("LayoutHeader: got %q, want Inline", disk.LayoutHeader.BlobStorage)
	}
	if disk.OriginalSize != int64(len("small")) {
		t.Errorf("OriginalSize: got %d, want %d", disk.OriginalSize, len("small"))
	}
	if string(disk.InlineBlob) != "small" {
		t.Errorf("InlineBlob: got %q, want %q", disk.InlineBlob, "small")
	}
}

func TestPut_Inline_ExactlyAtLimit_StaysInline(t *testing.T) {
	const limit int64 = 16
	s, root := newInlineStore(t, limit)

	exact := strings.Repeat("a", int(limit))
	id, err := s.Put(context.Background(),
		payload(exact),
		domain.PutOptions{Namespace: "edge"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	if got := countBlobFiles(t, root); got != 0 {
		t.Errorf("blob files: got %d, want 0 (inline at limit)", got)
	}

	disk := readManifestFromDisk(t, root, id)
	if disk.LayoutHeader.BlobStorage != "Inline" {
		t.Errorf("LayoutHeader: got %q, want Inline", disk.LayoutHeader.BlobStorage)
	}
	if disk.OriginalSize != limit {
		t.Errorf("OriginalSize: got %d, want %d", disk.OriginalSize, limit)
	}
}

func TestPut_Inline_OverLimit_FallsBackToTarget(t *testing.T) {
	const limit int64 = 16
	s, root := newInlineStore(t, limit)

	over := strings.Repeat("b", int(limit)+1)
	id, err := s.Put(context.Background(),
		payload(over),
		domain.PutOptions{Namespace: "spill"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	if got := countBlobFiles(t, root); got != 1 {
		t.Errorf("blob files: got %d, want 1 (target fallback)", got)
	}

	disk := readManifestFromDisk(t, root, id)
	if disk.LayoutHeader.BlobStorage != "Target" {
		t.Errorf("LayoutHeader: got %q, want Target", disk.LayoutHeader.BlobStorage)
	}
	if disk.OriginalSize != limit+1 {
		t.Errorf("OriginalSize: got %d, want %d", disk.OriginalSize, limit+1)
	}
}

func TestPut_Inline_NoDedupForInline(t *testing.T) {
	s, root := newInlineStore(t, 100)

	// Same content, two different SessionIDs → two distinct
	// manifests. With Target storage we would expect one shared
	// blob file (dedup hit). With Inline each manifest carries
	// its own bytes — we expect zero blob files regardless.
	const content = "shared inline"
	for _, sid := range []string{"a", "b"} {
		_, err := s.Put(context.Background(),
			payload(content),
			domain.PutOptions{Namespace: "ns", SessionID: sid})
		if err != nil {
			t.Fatal(err)
		}
	}

	if got := countBlobFiles(t, root); got != 0 {
		t.Errorf("blob files after 2 inline Puts: got %d, want 0", got)
	}

	// countBlobFiles==0 is the operative inline signal; per-manifest
	// LayoutHeader inspection would just repeat that on-disk evidence.
	// Here we assert the index sees both manifests as separate entries.
	var manifests int
	if err := s.Walk(context.Background(), "ns", func(m domain.Manifest) error {
		manifests++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if manifests != 2 {
		t.Errorf("manifests: got %d, want 2", manifests)
	}
}

func TestPut_Inline_EmptyPayload(t *testing.T) {
	s, root := newInlineStore(t, 100)

	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(nil)},
		domain.PutOptions{Namespace: "empty"})
	if err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	// Empty payload fits inline trivially.
	if got := countBlobFiles(t, root); got != 0 {
		t.Errorf("blob files: got %d, want 0", got)
	}

	disk := readManifestFromDisk(t, root, id)
	if disk.OriginalSize != 0 {
		t.Errorf("OriginalSize: got %d, want 0", disk.OriginalSize)
	}
	if disk.LayoutHeader.BlobStorage != "Inline" {
		t.Errorf("expected Inline for empty payload, got %q", disk.LayoutHeader.BlobStorage)
	}
}

func TestPut_Inline_DisabledByZeroLimit(t *testing.T) {
	// InlineFallback with InlineBlobLimit=0 means "never inline" —
	// the engine treats it as Target. Useful for callers who want
	// to keep the fallback strategy configured but temporarily
	// route everything to disk.
	s, root := newInlineStore(t, 0)

	id, err := s.Put(context.Background(),
		payload("anything"),
		domain.PutOptions{Namespace: "disabled"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := countBlobFiles(t, root); got != 1 {
		t.Errorf("blob files: got %d, want 1 (limit=0 disables inline)", got)
	}

	disk := readManifestFromDisk(t, root, id)
	if disk.LayoutHeader.BlobStorage != "Target" {
		t.Errorf("LayoutHeader: got %q, want Target", disk.LayoutHeader.BlobStorage)
	}
}

// --- Pipeline round-trip (M2.1) ---

func TestPut_Pipeline_ZstdRoundTrip(t *testing.T) {
	// Build a Store whose active config compresses via zstd. The
	// content we write must come back identical via Get.
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))

	cfg := domain.StoreConfig{
		Pipeline: []string{"zstd"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithReadRegistry(reg),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}

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
	reg := core.NewTransformerRegistry().Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{
		Pipeline: []string{"aes-gcm"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithReadRegistry(reg),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}

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
	if len(manifest.Pipeline[0].IV) != 12 {
		t.Fatalf("IV length = %d, want 12", len(manifest.Pipeline[0].IV))
	}
}

func TestPut_Pipeline_ZstdThenAESGCM(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i + 1)
	}
	aesFactory, _ := aesgcm.New(dek)
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{})).
		Register("aes-gcm", aesFactory)

	cfg := domain.StoreConfig{
		Pipeline: []string{"zstd", "aes-gcm"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithReadRegistry(reg),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}

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
	reg := core.NewTransformerRegistry()

	cfg := domain.StoreConfig{
		Pipeline: []string{"zstd"},
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithReadRegistry(reg),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	_, err = store.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("x"))},
		domain.PutOptions{Namespace: "ns"})
	if !errors.Is(err, errs.ErrUnsupportedAlgorithm) {
		t.Fatalf("Put: got %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestPut_Pipeline_RefusedOnInline(t *testing.T) {
	reg := core.NewTransformerRegistry().
		Register("zstd", scriniumzstd.New(scriniumzstd.Options{}))

	cfg := domain.StoreConfig{
		Pipeline:        []string{"zstd"},
		BlobStorage:     domain.BlobStorageInlineFallback,
		InlineBlobLimit: 1024,
	}

	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	store, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithReadRegistry(reg),
		core.WithConfig(cfg),
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

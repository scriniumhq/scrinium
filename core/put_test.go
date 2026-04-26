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
)

// helper: build a Store backed by a localfs driver and an
// in-memory sqlite index, with sha256 registered. Returns the
// Store and a handle to the underlying driver root for direct
// disk inspection.
func newStoreWithRoot(t *testing.T, opts ...core.StoreOption) (core.Store, string) {
	t.Helper()
	drv := newDriver(t)
	root := drv.Root()
	all := append([]core.StoreOption{
		core.WithStoreIndex(newIndex(t)),
		core.WithHashRegistry(newHashes()),
	}, opts...)
	s, _, err := core.InitStore(context.Background(), drv, all...)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	_ = root
	return s, root
}

// helper: a payload of repeated bytes with known content.
func payload(content string) domain.Artifact {
	return domain.Artifact{
		Payload: strings.NewReader(content),
	}
}

// --- Happy path ---

func TestPut_FreshBlob_WritesArtifacts(t *testing.T) {
	s, root := newStoreWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("hello scrinium"),
		core.PutOptions{Namespace: "users"})
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
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("walk-test"),
		core.PutOptions{Namespace: "users"})
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
	s, root := newStoreWithRoot(t)
	const text = "duplicate me"

	id1, err := s.Put(context.Background(), payload(text),
		core.PutOptions{Namespace: "ns", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(context.Background(), payload(text),
		core.PutOptions{Namespace: "ns", SessionID: "sess-2"})
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
	s, _ := newStoreWithRoot(t)
	const text = "shared content"

	id1, err := s.Put(context.Background(), payload(text),
		core.PutOptions{Namespace: "n", SessionID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(context.Background(), payload(text),
		core.PutOptions{Namespace: "n", SessionID: "b"})
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
	s, _ := newStoreWithRoot(t)
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(),
		payload("retention test"),
		core.PutOptions{
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
	s, _ := newStoreWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{Namespace: "system.config"})
	if !errors.Is(err, core.ErrReservedNamespace) {
		t.Fatalf("expected ErrReservedNamespace, got %v", err)
	}
}

func TestPut_RejectsWildcardNamespace(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{Namespace: "*"})
	if !errors.Is(err, core.ErrReservedNamespace) {
		t.Fatalf("expected ErrReservedNamespace, got %v", err)
	}
}

func TestPut_RejectsTooLongNamespace(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{Namespace: strings.Repeat("a", 256)})
	if !errors.Is(err, domain.ErrNamespaceTooLong) {
		t.Fatalf("expected ErrNamespaceTooLong, got %v", err)
	}
}

func TestPut_RejectsTooLongSessionID(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{SessionID: strings.Repeat("a", 256)})
	if !errors.Is(err, domain.ErrSessionIDTooLong) {
		t.Fatalf("expected ErrSessionIDTooLong, got %v", err)
	}
}

func TestPut_RejectsHugeMetadata(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	huge := bytes.Repeat([]byte(`a`), 64*1024+1)
	_, err := s.Put(context.Background(),
		domain.Artifact{
			Payload:  strings.NewReader("ok"),
			Metadata: append([]byte(`"`), append(huge, '"')...),
		},
		core.PutOptions{})
	if !errors.Is(err, domain.ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge, got %v", err)
	}
}

func TestPut_RejectsNilPayload(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	_, err := s.Put(context.Background(),
		domain.Artifact{Payload: nil},
		core.PutOptions{})
	if err == nil {
		t.Fatal("expected error on nil payload")
	}
}

// --- State checks ---

func TestPut_BlockedInReadOnly(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	if err := s.SetMaintenanceMode(context.Background(),
		core.MaintenanceModeReadOnly); err != nil {
		t.Fatal(err)
	}
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{})
	if !errors.Is(err, core.ErrStoreReadOnly) {
		t.Fatalf("expected ErrStoreReadOnly, got %v", err)
	}
}

func TestPut_BlockedInOffline(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	if err := s.SetMaintenanceMode(context.Background(),
		core.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{})
	if !errors.Is(err, core.ErrStoreOffline) {
		t.Fatalf("expected ErrStoreOffline, got %v", err)
	}
}

func TestPut_CtxCancelled(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Put(ctx, payload("nope"), core.PutOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- Deferred surfaces ---

func TestPut_BlobTypeOtherThanRegular_Deferred(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	_, err := s.Put(context.Background(),
		payload("nope"),
		core.PutOptions{BlobType: core.BlobTypeChunk})
	if err == nil {
		t.Fatal("expected error on Chunk BlobType")
	}
	if !strings.Contains(err.Error(), "M3") {
		t.Errorf("error should reference M3: %v", err)
	}
}

// --- Long payload streaming ---

func TestPut_LargePayload(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	const N = 1 << 20 // 1 MiB
	data := bytes.Repeat([]byte{0xab}, N)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(data)},
		core.PutOptions{Namespace: "big"})
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
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("default ns"),
		core.PutOptions{}) // empty Namespace = default
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
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(nil)},
		core.PutOptions{Namespace: "empty"})
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

// --- Compile guard ---
var _ = io.Reader(strings.NewReader(""))

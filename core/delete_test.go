package core_test

import (
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

// --- Happy paths ---

func TestDelete_TargetRemovesManifestAndDecrementsRefCount(t *testing.T) {
	s, root := newStoreWithRoot(t)
	id, err := s.Put(context.Background(), payload("delete me"), core.PutOptions{Namespace: "d"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Manifest file gone from disk.
	idStr := string(id)
	hex := strings.TrimPrefix(idStr, "sha256-")
	manifestPath := filepath.Join(root, "manifests", hex[:2], hex[2:4], idStr)
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("manifest file should be gone, stat err=%v", err)
	}

	// Walk no longer sees it.
	var seen int
	_ = s.Walk(context.Background(), "d", func(m domain.Manifest) error {
		seen++
		return nil
	})
	if seen != 0 {
		t.Errorf("Walk after delete: got %d, want 0", seen)
	}

	// Get returns ErrArtifactNotFound.
	if _, err := s.Get(context.Background(), id, core.GetOptions{}); !errors.Is(err, core.ErrArtifactNotFound) {
		t.Errorf("Get after delete: expected ErrArtifactNotFound, got %v", err)
	}

	// Blob file is still on disk — physical removal is GC territory (M3).
	var blobCount int
	_ = filepath.Walk(filepath.Join(root, "blobs"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			blobCount++
		}
		return nil
	})
	if blobCount != 1 {
		t.Errorf("blob file should remain after Delete (physical GC is M3): got %d", blobCount)
	}
}

func TestDelete_InlineRemovesManifestRow(t *testing.T) {
	s, root := newInlineStore(t, 100)
	id, err := s.Put(context.Background(), payload("inline delete"), core.PutOptions{Namespace: "d"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	idStr := string(id)
	hex := strings.TrimPrefix(idStr, "sha256-")
	manifestPath := filepath.Join(root, "manifests", hex[:2], hex[2:4], idStr)
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("inline manifest file should be gone, stat err=%v", err)
	}

	// Walk sees nothing — this is the assertion that catches the
	// pre-fix DeleteManifest bug (early-return for empty
	// manifest_blobs would leave the row in `manifests` for
	// inline artifacts).
	var seen int
	_ = s.Walk(context.Background(), "d", func(m domain.Manifest) error {
		seen++
		return nil
	})
	if seen != 0 {
		t.Errorf("Walk after inline delete: got %d, want 0 (DeleteManifest must remove the row even for inline manifests with no manifest_blobs edges)", seen)
	}
}

func TestDelete_SharedBlobKeepsRefCount(t *testing.T) {
	// Two artifacts share one blob. Deleting one must keep the
	// other readable — i.e. the physical blob stays, ref_count
	// drops from 2 to 1.
	s, root := newStoreWithRoot(t)
	const text = "shared content for delete"
	idA, err := s.Put(context.Background(), payload(text),
		core.PutOptions{Namespace: "ns", SessionID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.Put(context.Background(), payload(text),
		core.PutOptions{Namespace: "ns", SessionID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Fatalf("dedup test setup broken: ids equal")
	}

	if err := s.Delete(context.Background(), idA); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	// A is gone; B still readable.
	if _, err := s.Get(context.Background(), idA, core.GetOptions{}); !errors.Is(err, core.ErrArtifactNotFound) {
		t.Errorf("Get(A) after delete: expected ErrArtifactNotFound, got %v", err)
	}
	rh, err := s.Get(context.Background(), idB, core.GetOptions{})
	if err != nil {
		t.Fatalf("Get(B) after deleting A: %v", err)
	}
	got, _ := io.ReadAll(rh)
	rh.Close()
	if string(got) != text {
		t.Errorf("B payload after deleting A: got %q, want %q", got, text)
	}

	// Blob file still there.
	var blobCount int
	_ = filepath.Walk(filepath.Join(root, "blobs"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			blobCount++
		}
		return nil
	})
	if blobCount != 1 {
		t.Errorf("blob file count after deleting one of two referrers: got %d, want 1", blobCount)
	}
}

// --- Retention ---

func TestDelete_BlockedByActiveRetention(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(), payload("retained"),
		core.PutOptions{Namespace: "v", RetentionUntil: when})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, core.ErrRetentionNotExpired) {
		t.Fatalf("expected ErrRetentionNotExpired, got %v", err)
	}
}

func TestDelete_AllowedAfterRetentionExpires(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	// Retention in the past: expired the moment Put returns.
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(), payload("expired"),
		core.PutOptions{Namespace: "v", RetentionUntil: when})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), id); err != nil {
		t.Errorf("Delete after expiry: %v", err)
	}
}

// --- Errors ---

func TestDelete_NotFound(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	err := s.Delete(context.Background(),
		domain.ArtifactID("sha256-"+strings.Repeat("0", 64)))
	if !errors.Is(err, core.ErrArtifactNotFound) {
		t.Fatalf("expected ErrArtifactNotFound, got %v", err)
	}
}

func TestDelete_EmptyID(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	err := s.Delete(context.Background(), "")
	if !errors.Is(err, core.ErrArtifactNotFound) {
		t.Fatalf("expected ErrArtifactNotFound, got %v", err)
	}
}

func TestDelete_DoubleDeleteIsNotFound(t *testing.T) {
	// Second Delete on the same id returns ErrArtifactNotFound
	// (manifest file is gone after the first). Documented as
	// the natural CAS semantics in the §2.2 plan.
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(), payload("twice"), core.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, core.ErrArtifactNotFound) {
		t.Errorf("second Delete: expected ErrArtifactNotFound, got %v", err)
	}
}

// --- State / policy gates ---

func TestDelete_BlockedInReadOnly(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(), payload("ro"), core.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMode(context.Background(), core.MaintenanceModeReadOnly); err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, core.ErrStoreReadOnly) {
		t.Fatalf("expected ErrStoreReadOnly, got %v", err)
	}
}

func TestDelete_BlockedInOffline(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(), payload("off"), core.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMode(context.Background(), core.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, core.ErrStoreOffline) {
		t.Fatalf("expected ErrStoreOffline, got %v", err)
	}
}

func TestDelete_BlockedByDeletionPolicyNoDelete(t *testing.T) {
	drv := newDriver(t)
	cfg := domain.StoreConfig{
		DeletionPolicy: domain.DeletionPolicyNoDelete,
	}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
		core.WithHashRegistry(newHashes()),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	id, err := s.Put(context.Background(), payload("locked"), core.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, core.ErrDeletionForbidden) {
		t.Fatalf("expected ErrDeletionForbidden, got %v", err)
	}
}

func TestDelete_RetentionBeatsPolicy(t *testing.T) {
	// §2.2: retention is checked BEFORE policy. A NoDelete store
	// must still report ErrRetentionNotExpired (not Forbidden)
	// when both apply.
	drv := newDriver(t)
	cfg := domain.StoreConfig{DeletionPolicy: domain.DeletionPolicyNoDelete}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
		core.WithHashRegistry(newHashes()),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(), payload("both"),
		core.PutOptions{RetentionUntil: when})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, core.ErrRetentionNotExpired) {
		t.Fatalf("retention must beat policy; got %v", err)
	}
}

func TestDelete_CtxCancelled(t *testing.T) {
	s, _ := newStoreWithRoot(t)
	id, err := s.Put(context.Background(), payload("cx"), core.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.Delete(ctx, id)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

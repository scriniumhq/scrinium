package core_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
)

// --- Happy paths ---

func TestDelete_TargetRemovesManifestAndDecrementsRefCount(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("delete me"), domain.PutOptions{Namespace: "d"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	disk := storefx.OnDiskAt(root)

	// Manifest file gone from disk.
	if disk.ManifestExists(id) {
		t.Errorf("manifest file should be gone")
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

	// Get returns errs.ErrArtifactNotFound.
	if _, err := s.Get(context.Background(), id, domain.GetOptions{}); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get after delete: expected errs.ErrArtifactNotFound, got %v", err)
	}

	// Blob file is still on disk — physical removal is GC territory.
	if n := disk.BlobCount(); n != 1 {
		t.Errorf("blob file should remain after Delete (physical GC deferred): got %d", n)
	}
}

func TestDelete_InlineRemovesManifestRow(t *testing.T) {
	s, root := newInlineStore(t, 100)
	id, err := s.Put(context.Background(), payload("inline delete"), domain.PutOptions{Namespace: "d"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if storefx.OnDiskAt(root).ManifestExists(id) {
		t.Errorf("inline manifest file should be gone")
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
	s, root := storefx.InitWithRoot(t)
	const text = "shared content for delete"
	idA, err := s.Put(context.Background(), payload(text),
		domain.PutOptions{Namespace: "ns", SessionID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.Put(context.Background(), payload(text),
		domain.PutOptions{Namespace: "ns", SessionID: "b"})
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
	if _, err := s.Get(context.Background(), idA, domain.GetOptions{}); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get(A) after delete: expected errs.ErrArtifactNotFound, got %v", err)
	}
	rh, err := s.Get(context.Background(), idB, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get(B) after deleting A: %v", err)
	}
	got, _ := io.ReadAll(rh)
	rh.Close()
	if string(got) != text {
		t.Errorf("B payload after deleting A: got %q, want %q", got, text)
	}

	// Blob file still there.
	if n := storefx.OnDiskAt(root).BlobCount(); n != 1 {
		t.Errorf("blob file count after deleting one of two referrers: got %d, want 1", n)
	}
}

// --- Retention ---

func TestDelete_BlockedByActiveRetention(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(), payload("retained"),
		domain.PutOptions{Namespace: "v", RetentionUntil: when})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, errs.ErrRetentionNotExpired) {
		t.Fatalf("expected errs.ErrRetentionNotExpired, got %v", err)
	}
}

func TestDelete_AllowedAfterRetentionExpires(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	// Retention in the past: expired the moment Put returns.
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(), payload("expired"),
		domain.PutOptions{Namespace: "v", RetentionUntil: when})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), id); err != nil {
		t.Errorf("Delete after expiry: %v", err)
	}
}

// --- Errors ---

func TestDelete_NotFound(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	err := s.Delete(context.Background(),
		domain.ArtifactID("sha256-"+strings.Repeat("0", 64)))
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
	}
}

func TestDelete_EmptyID(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	err := s.Delete(context.Background(), "")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
	}
}

func TestDelete_DoubleDeleteIsNotFound(t *testing.T) {
	// Second Delete on the same id returns errs.ErrArtifactNotFound
	// (manifest file is gone after the first). Documented as
	// the natural CAS semantics in the §2.2 plan.
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("twice"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("second Delete: expected errs.ErrArtifactNotFound, got %v", err)
	}
}

// --- State / policy gates ---

func TestDelete_BlockedInReadOnly(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("ro"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, errs.ErrStoreReadOnly) {
		t.Fatalf("expected errs.ErrStoreReadOnly, got %v", err)
	}
}

func TestDelete_BlockedInOffline(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("off"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, errs.ErrStoreOffline) {
		t.Fatalf("expected errs.ErrStoreOffline, got %v", err)
	}
}

func TestDelete_BlockedByDeletionPolicyNoDelete(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{
		DeletionPolicy: domain.DeletionPolicyNoDelete,
	}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	id, err := s.Put(context.Background(), payload("locked"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, errs.ErrDeletionForbidden) {
		t.Fatalf("expected errs.ErrDeletionForbidden, got %v", err)
	}
}

func TestDelete_RetentionBeatsPolicy(t *testing.T) {
	// §2.2: retention is checked BEFORE policy. A NoDelete store
	// must still report errs.ErrRetentionNotExpired (not Forbidden)
	// when both apply.
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{DeletionPolicy: domain.DeletionPolicyNoDelete}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
		core.WithConfig(cfg),
	)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.Put(context.Background(), payload("both"),
		domain.PutOptions{RetentionUntil: when})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Delete(context.Background(), id)
	if !errors.Is(err, errs.ErrRetentionNotExpired) {
		t.Fatalf("retention must beat policy; got %v", err)
	}
}

func TestDelete_CtxCancelled(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("cx"), domain.PutOptions{})
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

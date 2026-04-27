package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

// --- MarkVerified ---

func TestMarkVerified_Updates(t *testing.T) {
	idx := newMemoryIndex(t)
	insertBlob(t, idx, "blob-1", "sha256-"+strings.Repeat("a", 64), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p"}, 1)

	// Truncate to the storage precision (RFC 3339 seconds, UTC) so
	// the round-trip Equal check below survives.
	now := time.Now().UTC().Truncate(time.Second)
	if err := idx.MarkVerified("blob-1", now); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}

	var ts string
	err := idx.db.QueryRowContext(context.Background(),
		`SELECT last_verified_at FROM blobs WHERE blob_ref = ?`, "blob-1",
	).Scan(&ts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseRFC3339(ts)
	if err != nil {
		t.Fatalf("parse stored timestamp: %v", err)
	}
	if !got.Equal(now) {
		t.Errorf("last_verified_at: got %v, want %v", got, now)
	}
}

func TestMarkVerified_MissingBlobIsNoOp(t *testing.T) {
	idx := newMemoryIndex(t)
	if err := idx.MarkVerified("nonexistent", time.Now()); err != nil {
		t.Errorf("missing blob should be no-op, got %v", err)
	}
}

// --- DeletePacked ---

func TestDeletePacked_RemovesAllEntriesForPack(t *testing.T) {
	idx := newMemoryIndex(t)

	// Build two packs with two entries each.
	pack1 := domain.Manifest{
		ArtifactID:   "pack-1",
		Type:         domain.ManifestTypePack,
		ContentHash:  "sha256-" + domain.ContentHash(strings.Repeat("1", 64)),
		BlobRef:      "pack-blob-1",
		OriginalSize: 4096,
		CreatedAt:    time.Now(),
	}
	if err := idx.IndexManifest(pack1, newPhysAddr("packs/p1"), nil, []domain.PackedEntry{
		{ArtifactID: "a1", BlobRef: "b1", BlobSize: 100, ContentHash: "sha256-" + domain.ContentHash(strings.Repeat("a", 64)), PipelineParams: []byte{}},
		{ArtifactID: "a2", BlobRef: "b2", BlobSize: 200, ContentHash: "sha256-" + domain.ContentHash(strings.Repeat("b", 64)), PipelineParams: []byte{}},
	}); err != nil {
		t.Fatalf("setup pack-1: %v", err)
	}

	pack2 := domain.Manifest{
		ArtifactID:   "pack-2",
		Type:         domain.ManifestTypePack,
		ContentHash:  "sha256-" + domain.ContentHash(strings.Repeat("2", 64)),
		BlobRef:      "pack-blob-2",
		OriginalSize: 4096,
		CreatedAt:    time.Now(),
	}
	if err := idx.IndexManifest(pack2, newPhysAddr("packs/p2"), nil, []domain.PackedEntry{
		{ArtifactID: "c1", BlobRef: "d1", BlobSize: 300, ContentHash: "sha256-" + domain.ContentHash(strings.Repeat("c", 64)), PipelineParams: []byte{}},
	}); err != nil {
		t.Fatalf("setup pack-2: %v", err)
	}

	// Sanity: 3 rows in packed_blobs total.
	if got := countRows(t, idx, "packed_blobs"); got != 3 {
		t.Fatalf("packed_blobs rows before: got %d, want 3", got)
	}

	// Delete entries of pack-blob-1 only.
	if err := idx.DeletePacked("pack-blob-1"); err != nil {
		t.Fatalf("DeletePacked: %v", err)
	}

	// Pack-1's entries gone, pack-2's untouched.
	if got := countRows(t, idx, "packed_blobs"); got != 1 {
		t.Errorf("packed_blobs rows after: got %d, want 1", got)
	}
	_, ok, err := idx.LookupPacked("c1")
	if err != nil || !ok {
		t.Errorf("pack-2 entry c1 should still exist: ok=%v err=%v", ok, err)
	}
}

func TestDeletePacked_Idempotent(t *testing.T) {
	idx := newMemoryIndex(t)
	// No packs at all; deleting a non-existent pack must succeed.
	if err := idx.DeletePacked("nonexistent-pack"); err != nil {
		t.Errorf("idempotent DeletePacked: %v", err)
	}
}

// --- VacuumInto ---

func TestVacuumInto_CreatesSnapshot(t *testing.T) {
	idx, _ := newDiskIndex(t)
	// Seed some data so the snapshot is a meaningful copy.
	insertBlob(t, idx, "blob-1", "sha256-"+strings.Repeat("a", 64), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p"}, 1)
	insertManifest(t, idx, domain.Manifest{
		ArtifactID: "art-1", Type: domain.ManifestTypeBlob, Namespace: "ns",
		BlobRef: "blob-1", CreatedAt: time.Now(),
	})

	dest := filepath.Join(t.TempDir(), "snap.db")
	if err := idx.VacuumInto(context.Background(), dest); err != nil {
		t.Fatalf("VacuumInto: %v", err)
	}

	// File exists.
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if st.Size() == 0 {
		t.Error("snapshot file is empty")
	}

	// Snapshot is a self-contained, openable database with the
	// same data.
	snapIface, err := NewStore(context.Background(), dest)
	if err != nil {
		t.Fatalf("NewStore snapshot: %v", err)
	}
	snap := snapIface.(*Index)

	if got := countRows(t, snap, "blobs"); got != 1 {
		t.Errorf("snapshot blobs: got %d, want 1", got)
	}
	if got := countRows(t, snap, "manifests"); got != 1 {
		t.Errorf("snapshot manifests: got %d, want 1", got)
	}
}

func TestVacuumInto_RejectsExistingFile(t *testing.T) {
	idx, _ := newDiskIndex(t)
	dest := filepath.Join(t.TempDir(), "exists.db")
	if err := os.WriteFile(dest, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := idx.VacuumInto(context.Background(), dest)
	if err == nil {
		t.Fatal("expected error on existing destination")
	}
}

func TestVacuumInto_RejectsMemoryDest(t *testing.T) {
	idx := newMemoryIndex(t)
	err := idx.VacuumInto(context.Background(), ":memory:")
	if err == nil {
		t.Fatal("expected error on :memory: destination")
	}
}

func TestVacuumInto_RejectsEmptyPath(t *testing.T) {
	idx := newMemoryIndex(t)
	err := idx.VacuumInto(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty path")
	}
}

func TestVacuumInto_CreatesParentDir(t *testing.T) {
	idx, _ := newDiskIndex(t)
	dest := filepath.Join(t.TempDir(), "deep", "nested", "snap.db")
	if err := idx.VacuumInto(context.Background(), dest); err != nil {
		t.Fatalf("VacuumInto with nested parent: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("snapshot not created: %v", err)
	}
}

// --- GetMeta / SetMeta ---

func TestSetMeta_GetMeta_RoundTrip(t *testing.T) {
	idx := newMemoryIndex(t)
	if err := idx.SetMeta("schema_notes", "v1: initial"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, err := idx.GetMeta("schema_notes")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if got != "v1: initial" {
		t.Errorf("value: got %q, want %q", got, "v1: initial")
	}
}

func TestSetMeta_Overwrites(t *testing.T) {
	idx := newMemoryIndex(t)
	if err := idx.SetMeta("k", "first"); err != nil {
		t.Fatal(err)
	}
	if err := idx.SetMeta("k", "second"); err != nil {
		t.Fatal(err)
	}
	got, _ := idx.GetMeta("k")
	if got != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
	// Still one row total.
	if got := countRows(t, idx, "store_meta"); got != 1 {
		t.Errorf("store_meta rows: got %d, want 1", got)
	}
}

func TestGetMeta_Missing(t *testing.T) {
	idx := newMemoryIndex(t)
	_, err := idx.GetMeta("never-set")
	if !errors.Is(err, errs.ErrMetaKeyNotFound) {
		t.Fatalf("expected errs.ErrMetaKeyNotFound, got %v", err)
	}
}

func TestSetMeta_BinarySafe(t *testing.T) {
	idx := newMemoryIndex(t)
	// Unicode, tabs, newlines, quotes — store_meta must survive
	// arbitrary text payloads.
	weird := "lineA\nlineB\tcol\u200b\"quoted'mixed"
	if err := idx.SetMeta("weird", weird); err != nil {
		t.Fatal(err)
	}
	got, err := idx.GetMeta("weird")
	if err != nil {
		t.Fatal(err)
	}
	if got != weird {
		t.Errorf("value mangled: got %q, want %q", got, weird)
	}
}

// --- Compile-time interface conformance ---

func TestIndex_ImplementsStoreIndex(t *testing.T) {
	// The compile-time check var _ domain.StoreIndex = (*Index)(nil)
	// in maintenance.go is the real guarantee; this test just
	// confirms it at runtime so a regression shows up in test
	// output, not just a build error.
	var _ core.StoreIndex = (*Index)(nil)
	idx := newMemoryIndex(t)
	var asInterface core.StoreIndex = idx
	if asInterface == nil {
		t.Fatal("Index does not satisfy domain.StoreIndex")
	}
}

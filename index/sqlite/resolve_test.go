package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

// helper: insert a blob row directly, bypassing IndexManifest.
// Lets resolve-side tests stay focused on the read paths.
func insertBlob(t *testing.T, idx *Index, ref, contentHash string, size int64, addr domain.PhysicalAddress, refCount int) {
	t.Helper()
	_, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO blobs (
			blob_ref, content_hash, original_size,
			physical_workspace, physical_path,
			pack_ref, pack_offset, pack_size,
			ref_count, last_verified_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		ref, contentHash, size,
		int(addr.Workspace), addr.Path,
		addr.PackRef, addr.Offset, addr.Size,
		refCount, fmtRFC3339(time.Now()),
	)
	if err != nil {
		t.Fatalf("insertBlob: %v", err)
	}
}

// --- Resolve ---

func TestResolve_Basic(t *testing.T) {
	idx := newMemoryIndex(t)
	insertBlob(t, idx, "blob-1", "sha256-"+strings.Repeat("a", 64), 1024,
		domain.PhysicalAddress{
			Workspace: domain.WorkspaceLocation,
			Path:      "blobs/aa/bb/blob-1",
		}, 1)

	addr, err := idx.Resolve("blob-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if addr.Workspace != domain.WorkspaceLocation {
		t.Errorf("Workspace: got %d, want %d", addr.Workspace, domain.WorkspaceLocation)
	}
	if addr.Path != "blobs/aa/bb/blob-1" {
		t.Errorf("Path: got %q, want %q", addr.Path, "blobs/aa/bb/blob-1")
	}
}

func TestResolve_Missing(t *testing.T) {
	idx := newMemoryIndex(t)
	_, err := idx.Resolve("nonexistent")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
	}
}

func TestResolve_PackedBlob(t *testing.T) {
	idx := newMemoryIndex(t)
	// A blob that lives inside a pack volume has its pack_ref/
	// pack_offset/pack_size populated; physical_path points to the
	// pack file.
	insertBlob(t, idx, "blob-in-pack", "sha256-"+strings.Repeat("p", 64), 4096,
		domain.PhysicalAddress{
			Workspace: domain.WorkspaceLocation,
			Path:      "packs/pack-1",
			PackRef:   "pack-blob-1",
			Offset:    8192,
			Size:      4096,
		}, 1)

	addr, err := idx.Resolve("blob-in-pack")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if addr.PackRef != "pack-blob-1" {
		t.Errorf("PackRef: got %q, want %q", addr.PackRef, "pack-blob-1")
	}
	if addr.Offset != 8192 {
		t.Errorf("Offset: got %d, want 8192", addr.Offset)
	}
}

// --- ExistsByContent ---

func TestExistsByContent_Hit(t *testing.T) {
	idx := newMemoryIndex(t)
	hash := domain.ContentHash("sha256-" + strings.Repeat("a", 64))
	insertBlob(t, idx, "blob-1", string(hash), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p"}, 1)

	ref, ok, err := idx.ExistsByContent(hash, 1024)
	if err != nil {
		t.Fatalf("ExistsByContent: %v", err)
	}
	if !ok {
		t.Fatal("expected found")
	}
	if ref != "blob-1" {
		t.Errorf("ref: got %q, want %q", ref, "blob-1")
	}
}

func TestExistsByContent_Miss(t *testing.T) {
	idx := newMemoryIndex(t)
	ref, ok, err := idx.ExistsByContent("sha256-deadbeef", 999)
	if err != nil {
		t.Fatalf("ExistsByContent: %v", err)
	}
	if ok {
		t.Error("expected not found")
	}
	if ref != "" {
		t.Errorf("ref: got %q, want empty", ref)
	}
}

// TestExistsByContent_HashHitSizeMiss verifies that the composite
// key (content_hash, original_size) is enforced. Two blobs sharing
// a content_hash but differing in size are distinct entries and
// must not match each other.
func TestExistsByContent_HashHitSizeMiss(t *testing.T) {
	idx := newMemoryIndex(t)
	hash := domain.ContentHash("sha256-" + strings.Repeat("x", 64))
	insertBlob(t, idx, "blob-1k", string(hash), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p1"}, 1)

	// Same hash, different size — must NOT match.
	_, ok, err := idx.ExistsByContent(hash, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("hash-only match leaked through size filter")
	}
}

// --- ExistsByHash ---

func TestExistsByHash_Hit(t *testing.T) {
	idx := newMemoryIndex(t)
	hash := domain.ContentHash("sha256-" + strings.Repeat("a", 64))
	insertBlob(t, idx, "blob-1", string(hash), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p"}, 1)

	status, err := idx.ExistsByHash(hash)
	if err != nil {
		t.Fatalf("ExistsByHash: %v", err)
	}
	if status != domain.BlobExists {
		t.Errorf("status: got %d, want BlobExists", status)
	}
}

func TestExistsByHash_Miss(t *testing.T) {
	idx := newMemoryIndex(t)
	status, err := idx.ExistsByHash("sha256-deadbeef")
	if err != nil {
		t.Fatalf("ExistsByHash: %v", err)
	}
	if status != domain.BlobNotFound {
		t.Errorf("status: got %d, want BlobNotFound", status)
	}
}

// TestExistsByHash_IgnoresSize verifies that two blobs sharing a
// content_hash are both reachable through ExistsByHash regardless
// of their size — chunker.Wrapper does not know the size up front,
// it only checks "have we seen this content before?".
func TestExistsByHash_IgnoresSize(t *testing.T) {
	idx := newMemoryIndex(t)
	hash := domain.ContentHash("sha256-" + strings.Repeat("x", 64))
	insertBlob(t, idx, "blob-1k", string(hash), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p1"}, 1)
	insertBlob(t, idx, "blob-2k", string(hash), 2048,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p2"}, 1)

	status, err := idx.ExistsByHash(hash)
	if err != nil {
		t.Fatalf("ExistsByHash: %v", err)
	}
	if status != domain.BlobExists {
		t.Errorf("status: got %d, want BlobExists", status)
	}
}

// --- GetRefCount ---

func TestGetRefCount_Basic(t *testing.T) {
	idx := newMemoryIndex(t)
	insertBlob(t, idx, "blob-1", "sha256-"+strings.Repeat("a", 64), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p"}, 3)

	n, err := idx.GetRefCount("blob-1")
	if err != nil {
		t.Fatalf("GetRefCount: %v", err)
	}
	if n != 3 {
		t.Errorf("ref_count: got %d, want 3", n)
	}
}

func TestGetRefCount_Missing(t *testing.T) {
	idx := newMemoryIndex(t)
	_, err := idx.GetRefCount("nonexistent")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
	}
}

// TestGetRefCount_Zero verifies the difference between "missing"
// and "ref_count = 0". The latter is the legitimate orphan state
// that the GC reaper iterates over.
func TestGetRefCount_Zero(t *testing.T) {
	idx := newMemoryIndex(t)
	insertBlob(t, idx, "orphan-1", "sha256-"+strings.Repeat("a", 64), 1024,
		domain.PhysicalAddress{Workspace: domain.WorkspaceLocation, Path: "p"}, 0)

	n, err := idx.GetRefCount("orphan-1")
	if err != nil {
		t.Fatalf("GetRefCount: %v", err)
	}
	if n != 0 {
		t.Errorf("ref_count: got %d, want 0", n)
	}
}

// --- LookupPacked ---

func TestLookupPacked_Hit(t *testing.T) {
	idx := newMemoryIndex(t)

	// Register a pack volume with two packed entries via the
	// regular pack-manifest path; we want the realistic insertion
	// shape, not a hand-built INSERT.
	packManifest := domain.Manifest{
		ArtifactID:   "pack-1",
		Type:         domain.ManifestTypePack,
		ContentHash:  "sha256-" + domain.ContentHash(strings.Repeat("p", 64)),
		BlobRef:      "pack-blob-1",
		OriginalSize: 65536,
		CreatedAt:    time.Now(),
	}
	entries := []domain.PackedEntry{
		{
			ArtifactID:     "art-p1",
			BlobRef:        "blob-p1",
			ManifestOffset: 0,
			ManifestSize:   200,
			BlobOffset:     200,
			BlobSize:       1024,
			ContentHash:    domain.ContentHash("sha256-" + strings.Repeat("1", 64)),
			PipelineParams: []byte{0xde, 0xad, 0xbe, 0xef},
		},
	}
	if err := idx.IndexManifest(packManifest, newPhysAddr("packs/pack-1"), nil, entries); err != nil {
		t.Fatalf("setup: %v", err)
	}

	info, ok, err := idx.LookupPacked("art-p1")
	if err != nil {
		t.Fatalf("LookupPacked: %v", err)
	}
	if !ok {
		t.Fatal("expected packed entry to be found")
	}
	if info.PackBlobRef != "pack-blob-1" {
		t.Errorf("PackBlobRef: got %q, want %q", info.PackBlobRef, "pack-blob-1")
	}
	if info.ManifestOffset != 0 || info.ManifestSize != 200 {
		t.Errorf("manifest range: got [%d, %d), want [0, 200)", info.ManifestOffset, info.ManifestSize)
	}
	if info.BlobOffset != 200 || info.BlobSize != 1024 {
		t.Errorf("blob range: got [%d, %d), want [200, 1024)", info.BlobOffset, info.BlobSize)
	}
	if len(info.PipelineParams) != 4 || info.PipelineParams[0] != 0xde {
		t.Errorf("PipelineParams round-trip lost bytes: got %v", info.PipelineParams)
	}
}

func TestLookupPacked_Miss(t *testing.T) {
	idx := newMemoryIndex(t)
	_, ok, err := idx.LookupPacked("not-packed")
	if err != nil {
		t.Fatalf("LookupPacked: %v", err)
	}
	if ok {
		t.Error("expected not found for non-packed artifact")
	}
}

// TestLookupPacked_NilParams verifies the COALESCE on
// pipeline_params returns an empty slice when the column is NULL,
// rather than scanning into a nil slice that the caller might
// confuse with "missing data".
func TestLookupPacked_NilParams(t *testing.T) {
	idx := newMemoryIndex(t)

	// Direct INSERT with NULL pipeline_params.
	_, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO packed_blobs (
			artifact_id, pack_blob_ref, blob_ref,
			manifest_offset, manifest_size,
			blob_offset, blob_size,
			content_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"art-null-params", "pack-blob", "inner-blob",
		0, 100, 100, 200,
		"sha256-"+strings.Repeat("0", 64),
	)
	if err != nil {
		t.Fatal(err)
	}

	info, ok, err := idx.LookupPacked("art-null-params")
	if err != nil {
		t.Fatalf("LookupPacked: %v", err)
	}
	if !ok {
		t.Fatal("expected found")
	}
	// COALESCE turns NULL into x'' which scans into an empty (not
	// nil) []byte. Either is acceptable per Go's []byte semantics;
	// the important property is no panic and a length of 0.
	if len(info.PipelineParams) != 0 {
		t.Errorf("PipelineParams: got %d bytes, want 0", len(info.PipelineParams))
	}
}

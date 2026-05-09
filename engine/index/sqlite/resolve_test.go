package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/engine/domain"
)

// Most Resolve / ExistsByContent / ExistsByHash / GetRefCount /
// LookupPacked tests live in the conformance suite at
// internal/testutil/indextest. This file is for sqlite-specific
// behaviour only.

// TestResolve_PackedBlob_FromBlobRow exercises the SQL branch
// where a blobs row carries pack_ref/pack_offset/pack_size and
// Resolve must return them in PhysicalAddress. This is glass-box
// because the row shape is not produced by the public API path
// (IndexManifest of a pack manifest creates ONE blobs row for the
// pack blob itself; the embedded blobs live in packed_blobs and
// are reached through LookupPacked, not Resolve). Direct INSERT
// stages the row to verify the columns flow through Scan
// correctly.
func TestResolve_PackedBlob_FromBlobRow(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)
	insertBlob(t, idx, "blob-in-pack",
		"sha256-"+strings.Repeat("p", 64), 4096,
		domain.PhysicalAddress{
			Workspace: domain.WorkspaceLocation,
			Path:      "packs/pack-1",
			PackRef:   "pack-blob-1",
			Offset:    8192,
			Size:      4096,
		}, 1)

	addr, err := idx.Resolve(ctx, "blob-in-pack")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if addr.PackRef != "pack-blob-1" {
		t.Errorf("PackRef: got %q, want pack-blob-1", addr.PackRef)
	}
	if addr.Offset != 8192 {
		t.Errorf("Offset: got %d, want 8192", addr.Offset)
	}
	if addr.Size != 4096 {
		t.Errorf("Size: got %d, want 4096", addr.Size)
	}
}

// TestLookupPacked_NilParams verifies the COALESCE on
// pipeline_params returns an empty slice when the column is NULL,
// rather than scanning into a nil slice that the caller might
// confuse with "missing data". Glass-box: we INSERT directly with
// NULL pipeline_params, which is not reachable through
// IndexManifest (which always supplies a []byte, even empty).
func TestLookupPacked_NilParams(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)

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

	info, ok, err := idx.LookupPacked(ctx, "art-null-params")
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

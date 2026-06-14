package sqlite

import (
	"strings"
	"testing"

	"scrinium.dev/domain"
)

// Most Resolve / ExistsByContent / ExistsByHash / GetRefCount
// tests live in the conformance suite at
// internal/testutil/indextest. This file is for sqlite-specific
// behaviour only.

// TestResolve_PackedBlob_FromBlobRow exercises the SQL branch
// where a blobs row carries pack_ref/pack_offset/pack_size and
// Resolve must return them in PhysicalAddress. This is glass-box
// because the row shape is not produced by the public API path
// (a blob inside a pack is placed there by the bundler, ADR-86).
// Direct INSERT
// stages the row to verify the columns flow through Scan
// correctly.
func TestResolve_PackedBlob_FromBlobRow(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)
	insertBlob(t, idx, "blob-in-pack",
		"sha256-"+strings.Repeat("p", 64), 4096,
		domain.PhysicalAddress{
			Path:    "packs/pack-1",
			PackRef: "pack-blob-1",
			Offset:  8192,
			Size:    4096,
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

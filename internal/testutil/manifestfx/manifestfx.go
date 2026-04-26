// Package manifestfx supplies domain.Manifest and core.PhysicalAddress
// builders used across tests. Centralised so the project's "what
// counts as a valid manifest for round-trip tests" definition lives
// in exactly one place; if the schema grows a required field, the
// fix lands here and every test compiles again.
package manifestfx

import (
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
)

// sha256-padded hashes used in test manifests. The "aaa..." and
// "bbb..." patterns are intentional: they are obviously
// synthetic, never accidentally collide with a real hash, and
// stay stable across runs so test failures diff cleanly.
var (
	contentHashAaa = "sha256-" + strings.Repeat("a", 64)
	blobRefBbb     = "sha256-" + strings.Repeat("b", 64)
)

// Sample returns a minimal valid blob manifest suitable for
// round-trip tests in manifestcodec and similar low-level code.
// All required fields are populated with deterministic values;
// CreatedAt is fixed (not time.Now()) so the produced manifest is
// byte-stable across runs.
func Sample() domain.Manifest {
	return domain.Manifest{
		Type:         domain.ManifestTypeBlob,
		Namespace:    "users",
		SessionID:    "sess-1",
		CreatedAt:    time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		ContentHash:  domain.ContentHash(contentHashAaa),
		OriginalSize: 4096,
		BlobRef:      domain.BlobRef(blobRefBbb),
		LayoutHeader: domain.LayoutHeader{BlobStorage: "Target"},
		Pipeline:     nil,
	}
}

// Blob returns a small blob manifest with caller-supplied id and
// blobRef but otherwise default fields. Used by index tests that
// exercise the (artifact_id, blob_ref) relationship and don't care
// about the rest of the manifest body.
//
// CreatedAt is time.Now() — these tests check timestamps for
// "looks recent" rather than byte stability, so wall-clock time
// is the right default.
func Blob(id, blobRef string) domain.Manifest {
	return domain.Manifest{
		ArtifactID:   domain.ArtifactID(id),
		Type:         domain.ManifestTypeBlob,
		Namespace:    "test",
		ContentHash:  domain.ContentHash(contentHashAaa),
		BlobRef:      domain.BlobRef(blobRef),
		OriginalSize: 1024,
		CreatedAt:    time.Now(),
	}
}

// PhysAddr is a one-liner for tests that need a Location-workspace
// address — by far the most common shape. Tests that need Host
// workspace or pack offsets construct PhysicalAddress directly.
func PhysAddr(path string) core.PhysicalAddress {
	return core.PhysicalAddress{
		Workspace: core.WorkspaceLocation,
		Path:      path,
	}
}

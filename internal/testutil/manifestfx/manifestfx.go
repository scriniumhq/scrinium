// Package manifestfx supplies domain.Manifest and domain.PhysicalAddress
// builders for tests.
package manifestfx

import (
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/domain"
)

// Synthetic hashes used in fixtures. Cannot be const because
// strings.Repeat is a runtime call.
var (
	contentHashAaa = "sha256-" + strings.Repeat("a", 64)
	blobRefBbb     = "sha256-" + strings.Repeat("b", 64)
)

// Sample returns a minimal valid blob manifest with a fixed
// CreatedAt — byte-stable across runs for round-trip tests.
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
	}
}

// Blob returns a small blob manifest with caller-supplied id and
// blobRef. CreatedAt is time.Now() — fine for index tests that
// don't check byte stability.
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

// PhysAddr is a Location-workspace address at path.
func PhysAddr(path string) domain.PhysicalAddress {
	return domain.PhysicalAddress{
		Workspace: domain.WorkspaceLocation,
		Path:      path,
	}
}

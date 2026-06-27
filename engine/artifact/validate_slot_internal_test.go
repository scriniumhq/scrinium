package artifact

import (
	"errors"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// validateSlot is the structural gate at the encode boundary (ADR-92/104):
// a manifest is exactly one kind and carries the structure its kind requires.
func TestValidateSlot(t *testing.T) {
	var (
		md        = "sha256-" + strings.Repeat("c", 64)
		handle    = domain.ArtifactID(strings.Repeat("e", 64))
		blob      = []domain.BlobRef{domain.BlobRef(strings.Repeat("b", 64))}
		nonce     = []byte("0123456789abcdef")
		inline    = []byte("envelope-bytes")
		inlineHdr = domain.LayoutHeader{BlobStorage: domain.LayoutInline}
	)

	cases := []struct {
		name    string
		m       domain.Manifest
		wantErr bool
	}{
		// Valid: one kind, complete structure.
		{"user blob-backed", domain.Manifest{ArtifactID: handle, IdentityMetaHash: md, BlobRefs: blob}, false},
		{"user inline, no blob_refs", domain.Manifest{ArtifactID: handle, IdentityMetaHash: md, LayoutHeader: inlineHdr, InlineBlob: inline}, false},
		{"user identity via nonce only", domain.Manifest{ArtifactID: handle, IdentityNonce: nonce, BlobRefs: blob}, false},
		{"system inline, no blob_refs", domain.Manifest{Name: "config.1", LayoutHeader: inlineHdr, InlineBlob: inline}, false},
		{"container blob-backed", domain.Manifest{BlobRefs: blob}, false},

		// Invalid: two slots at once.
		{"both slots filled", domain.Manifest{ArtifactID: handle, Name: "config.1", IdentityMetaHash: md, InlineBlob: inline}, true},

		// Invalid: a user handle with no identity-meta cannot exist (the
		// handle is PRF(NK, cd‖md)).
		{"user without identity-meta", domain.Manifest{ArtifactID: handle, BlobRefs: blob}, true},

		// Invalid: a system artifact missing or contradicting its shape.
		{"system without inline", domain.Manifest{Name: "config.1", BlobRefs: blob}, true},
		{"system with identity-meta", domain.Manifest{Name: "config.1", LayoutHeader: inlineHdr, InlineBlob: inline, IdentityMetaHash: md}, true},
		{"system with identity nonce", domain.Manifest{Name: "config.1", LayoutHeader: inlineHdr, InlineBlob: inline, IdentityNonce: nonce}, true},
		{"system not inline (Target layout)", domain.Manifest{Name: "config.1", LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget}, InlineBlob: inline}, true},

		// Invalid: inline content is embedded, not a physical blob — it
		// carries no blob_ref (ADR-66/92, Option A).
		{"system inline carries blob_refs", domain.Manifest{Name: "config.1", LayoutHeader: inlineHdr, InlineBlob: inline, BlobRefs: blob}, true},
		{"user inline carries blob_refs", domain.Manifest{ArtifactID: handle, IdentityMetaHash: md, LayoutHeader: inlineHdr, InlineBlob: inline, BlobRefs: blob}, true},

		// Invalid: a container contradicting its blob-backed, handle-less shape.
		{"container without blob_refs", domain.Manifest{}, true},
		{"container with inline", domain.Manifest{BlobRefs: blob, InlineBlob: inline}, true},
		{"container with identity-meta", domain.Manifest{BlobRefs: blob, IdentityMetaHash: md}, true},
		{"container with identity nonce", domain.Manifest{BlobRefs: blob, IdentityNonce: nonce}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSlot(tc.m)
			switch {
			case tc.wantErr && !errors.Is(err, errs.ErrInvalidManifestSlot):
				t.Fatalf("want ErrInvalidManifestSlot, got %v", err)
			case !tc.wantErr && err != nil:
				t.Fatalf("valid manifest rejected: %v", err)
			}
		})
	}
}

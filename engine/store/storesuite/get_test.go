// Get behaviour not covered by the round-trip property (category 4) or
// the cross-operation guard tables (category 6): random access for both
// layouts, on-disk integrity detection (corrupt manifest / missing blob),
// and ReadHandle semantics. Round-trip itself lives in properties_test.go
// and model_test.go; not-found / empty-id / state guards / ctx live in
// guards_test.go.

package storesuite

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// TestGet_ReadAt: random access mid-stream for both layouts. Target
// blobs must report SupportsRandomAccess; inline payloads serve ReadAt
// from the manifest.
func TestGet_ReadAt(t *testing.T) {
	cases := []struct {
		name    string
		inline  bool
		content string
		off     int64
		want    string
	}{
		{"target mid-stream", false, "abcdefghijklmnop", 5, "fghi"},
		{"inline mid-stream", true, "abcdefghij", 4, "efg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s store.Store
			if tc.inline {
				s, _ = storefx.InitInline(t, 100)
			} else {
				s, _ = storefx.InitWithRoot(t)
			}
			id, err := s.Put(context.Background(), artifactfx.Payload(tc.content))
			if err != nil {
				t.Fatal(err)
			}
			rh, err := s.Get(context.Background(), id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer rh.Close()

			if !tc.inline && !rh.SupportsRandomAccess() {
				t.Fatal("Target blob expected to support random access")
			}
			buf := make([]byte, len(tc.want))
			n, err := rh.ReadAt(buf, tc.off)
			if err != nil {
				t.Fatalf("ReadAt: %v", err)
			}
			if n != len(tc.want) || string(buf) != tc.want {
				t.Errorf("got n=%d buf=%q, want n=%d buf=%q", n, buf, len(tc.want), tc.want)
			}
		})
	}
}

// TestGet_Integrity: on-disk tampering is detected. A flipped byte in
// the manifest fails Get with ErrCorruptedManifest; a missing blob
// lets Get (manifest-only) succeed but fails the first Read with
// ErrCorruptedBlob.
func TestGet_Integrity(t *testing.T) {
	t.Run("corrupted manifest", func(t *testing.T) {
		s, root := storefx.InitWithRoot(t)
		id, err := s.Put(context.Background(), artifactfx.Payload("tamper me"))
		if err != nil {
			t.Fatal(err)
		}
		// The manifest file is named by its ManifestDigest, resolved from
		// the floating handle through the store.
		path := storefx.OnDiskAt(root).ManifestPath(storekit.MustDigest(t, s, id))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		if len(raw) < 10 {
			t.Fatalf("manifest unexpectedly short: %d bytes", len(raw))
		}
		raw[len(raw)-2] ^= 0x01
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("rewrite manifest: %v", err)
		}
		if _, err := s.Get(context.Background(), id); !errors.Is(err, errs.ErrCorruptedManifest) {
			t.Fatalf("got %v, want errs.ErrCorruptedManifest", err)
		}
	})

	t.Run("missing blob", func(t *testing.T) {
		s, root := storefx.InitWithRoot(t)
		id, err := s.Put(context.Background(), artifactfx.Payload("blob will vanish"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range storefx.OnDiskAt(root).BlobFiles() {
			_ = os.Remove(p)
		}
		// Get reads only the manifest, so it still succeeds; the
		// failure surfaces on the first Read.
		rh, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get with missing blob should succeed: %v", err)
		}
		defer rh.Close()
		if _, err := io.ReadAll(rh); !errors.Is(err, errs.ErrCorruptedBlob) {
			t.Fatalf("Read: got %v, want errs.ErrCorruptedBlob", err)
		}
	})
}

// TestGet_ReadHandleSemantics: the manifest is available before the
// first Read, and Close is idempotent.
func TestGet_ReadHandleSemantics(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), artifactfx.Payload("semantics"),
		domain.WithSession("sess-x"))
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	m := rh.Manifest()
	if m.ArtifactID != id {
		t.Errorf("ArtifactID: got %q, want %q", m.ArtifactID, id)
	}
	if m.SessionID != "sess-x" {
		t.Errorf("manifest SessionID: got %q, want sess-x", m.SessionID)
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutTarget {
		t.Errorf("LayoutHeader: got %q, want Target", m.LayoutHeader.BlobStorage)
	}

	if err := rh.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rh.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

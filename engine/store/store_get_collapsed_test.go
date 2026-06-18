// Collapsed Get tests. Round-trip (target + inline, empty + non-empty)
// is covered by the seeded round-trip property and the model test;
// not-found / empty-id, state guards and ctx cancellation moved to the
// cross-operation tables. What remains here is Get-specific behaviour
// no property covers: random access, on-disk integrity detection, and
// ReadHandle semantics.
//
// Replaces the previous store_get_test.go in full.

package store_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	storefx2 "scrinium.dev/testutil/storefx"
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
				s, _ = newInlineStore(t, 100)
			} else {
				s, _ = storefx2.InitWithRoot(t)
			}
			id, err := s.Put(context.Background(), payload(tc.content))
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
		s, root := storefx2.InitWithRoot(t)
		id, err := s.Put(context.Background(), payload("tamper me"))
		if err != nil {
			t.Fatal(err)
		}
		// The manifest file is named by its ManifestDigest, resolved from
		// the floating handle through the store.
		path := storefx2.OnDiskAt(root).ManifestPath(mustDigest(t, s, id))
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
		s, root := storefx2.InitWithRoot(t)
		id, err := s.Put(context.Background(), payload("blob will vanish"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range storefx2.OnDiskAt(root).BlobFiles() {
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
	s, _ := storefx2.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("semantics"),
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

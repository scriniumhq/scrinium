// New/merged Put tests. These replace the deleted example tests per
// TESTING.md: behavioural coverage (round-trip, dedup, Walk visibility)
// now lives in the seeded properties and the model test; what remains
// here is the on-disk/layout sanity, the shared-blob contract, and the
// genuinely enumerable tables (inline policy, input validation).
//
// Keep alongside the untouched TestPut_Pipeline_*, _BlobTypeDeferred,
// _LargePayload, _DefaultNamespace. The helper newInlineStore (defined
// in this package) is reused by TestPut_InlinePolicy.

package store_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	storefx2 "scrinium.dev/testutil/storefx"
)

// mustDigest resolves an artifact's on-disk ManifestDigest (the manifest
// filename) from its floating handle, via Get. Tests that inspect the
// physical manifest file need the digest, not the handle — the file is
// named by its digest and the index maps handle → digest.
func mustDigest(t *testing.T, s store.Store, id domain.ArtifactID) domain.ManifestDigest {
	t.Helper()
	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("mustDigest: Get %s: %v", id, err)
	}
	defer rh.Close()
	return rh.Manifest().Digest
}

// TestPut_OnDiskLayout is the single physical-layout sanity check: a
// fresh Put lands a manifest under manifests/, exactly one blob under
// blobs/, and Capacity reflects both. (Behavioural correctness is
// covered by the seeded round-trip and the model test; this pins the
// on-disk shape and the Capacity surface, which nothing else asserts.)
func TestPut_OnDiskLayout(t *testing.T) {
	s, root := storefx2.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("hello scrinium"),
		domain.WithNamespace("users"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(string(id), "sha256-") {
		t.Errorf("ArtifactID prefix: got %q", id)
	}

	disk := storefx2.OnDiskAt(root)
	digest := mustDigest(t, s, id)
	if !disk.ManifestExists(digest) {
		t.Errorf("manifest not on disk at %s", disk.ManifestPath(digest))
	}
	if n := disk.BlobCount(); n != 1 {
		t.Errorf("blobs on disk: got %d, want 1", n)
	}

	info, err := s.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 1 || info.BlobCount != 1 {
		t.Errorf("Capacity: got artifacts=%d blobs=%d, want 1/1",
			info.ArtifactCount, info.BlobCount)
	}
}

// TestPut_SharedBlobAcrossArtifacts pins the shared-blob contract:
// identical content under two different SessionIDs produces two
// distinct artifacts (distinct manifests, distinct IDs) that share a
// single on-disk blob, with the staging directory cleaned. This is the
// distinct-artifacts-share-one-blob case the content-addressing
// property does not cover (that one puts the same content under the
// same identity and expects the same ID).
func TestPut_SharedBlobAcrossArtifacts(t *testing.T) {
	s, root := storefx2.InitWithRoot(t)
	const text = "shared content"

	id1, err := s.Put(context.Background(), payload(text),
		domain.WithNamespace("n"), domain.WithSession("a"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(context.Background(), payload(text),
		domain.WithNamespace("n"), domain.WithSession("b"))
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("different SessionID must produce different ArtifactIDs, got %q", id1)
	}

	disk := storefx2.OnDiskAt(root)
	if n := disk.BlobCount(); n != 1 {
		t.Errorf("shared content should leave 1 blob, got %d", n)
	}
	if files := disk.StagingFiles(); len(files) > 0 {
		t.Errorf("staging directory not cleaned: %d entries", len(files))
	}

	var seen int
	if err := s.Walk(context.Background(), "n", func(domain.Manifest) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Errorf("Walk returned %d manifests, want 2", seen)
	}
}

// TestPut_InputValidation collapses the per-field rejection tests into
// one table. Each case Puts a single artifact and expects a specific
// sentinel (or, for the nil payload, any error).
func TestPut_InputValidation(t *testing.T) {
	huge := bytes.Repeat([]byte("a"), 64*1024+1)

	cases := []struct {
		name    string
		art     domain.Artifact
		opts    []domain.PutOption
		wantErr error // nil means "any non-nil error"
	}{
		{"reserved system namespace", payload("x"),
			[]domain.PutOption{domain.WithNamespace("system.config")}, errs.ErrReservedNamespace},
		{"wildcard namespace", payload("x"),
			[]domain.PutOption{domain.WithNamespace("*")}, errs.ErrReservedNamespace},
		{"namespace too long", payload("x"),
			[]domain.PutOption{domain.WithNamespace(strings.Repeat("a", 256))}, errs.ErrNamespaceTooLong},
		{"session id too long", payload("x"),
			[]domain.PutOption{domain.WithSession(domain.SessionID(strings.Repeat("a", 256)))}, errs.ErrSessionIDTooLong},
		{"ext too large",
			domain.Artifact{Payload: strings.NewReader("ok"), Ext: append([]byte(`"`), append(huge, '"')...)},
			nil, errs.ErrExtTooLarge},
		{"nil payload", domain.Artifact{Payload: nil}, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := storefx2.InitWithRoot(t)
			_, err := s.Put(context.Background(), tc.art, tc.opts...)
			if tc.wantErr == nil {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestPut_InlinePolicy collapses the Inline mode boundary tests:
// content at or below the limit stays inline (no blob file); over the
// limit it falls back to a Target blob; a zero limit disables inlining
// entirely. The final sub-test pins that inline manifests do not dedup
// (two identical inline Puts leave zero blob files, two manifests).
func TestPut_InlinePolicy(t *testing.T) {
	const limit int64 = 16

	cases := []struct {
		name       string
		limit      int64
		content    string
		wantBlobs  int
		wantLayout string
	}{
		{"below limit stays inline", 100, "small", 0, domain.LayoutInline},
		{"exactly at limit stays inline", limit, strings.Repeat("a", int(limit)), 0, domain.LayoutInline},
		{"over limit falls back to target", limit, strings.Repeat("b", int(limit)+1), 1, domain.LayoutTarget},
		{"empty payload is inline", 100, "", 0, domain.LayoutInline},
		{"zero limit disables inline", 0, "anything", 1, domain.LayoutTarget},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, root := newInlineStore(t, tc.limit)
			id, err := s.Put(context.Background(),
				payload(tc.content),
				domain.WithNamespace("inline"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			disk := storefx2.OnDiskAt(root)
			if n := disk.BlobCount(); n != tc.wantBlobs {
				t.Errorf("blob files: got %d, want %d", n, tc.wantBlobs)
			}
			m := disk.ReadManifest(t, mustDigest(t, s, id))
			if m.LayoutHeader.BlobStorage != tc.wantLayout {
				t.Errorf("LayoutHeader: got %q, want %q", m.LayoutHeader.BlobStorage, tc.wantLayout)
			}
			if m.OriginalSize != int64(len(tc.content)) {
				t.Errorf("OriginalSize: got %d, want %d", m.OriginalSize, len(tc.content))
			}
		})
	}

	t.Run("no dedup for inline", func(t *testing.T) {
		s, root := newInlineStore(t, 100)
		const content = "shared inline"
		for _, sid := range []domain.SessionID{"a", "b"} {
			if _, err := s.Put(context.Background(), payload(content),
				domain.WithNamespace("ns"), domain.WithSession(sid)); err != nil {
				t.Fatal(err)
			}
		}
		if n := storefx2.OnDiskAt(root).BlobCount(); n != 0 {
			t.Errorf("blob files after 2 inline Puts: got %d, want 0", n)
		}
		var manifests int
		if err := s.Walk(context.Background(), "ns", func(domain.Manifest) error {
			manifests++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if manifests != 2 {
			t.Errorf("manifests: got %d, want 2", manifests)
		}
	})
}

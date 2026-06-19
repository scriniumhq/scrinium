// Put mechanics not covered by the round-trip property (category 4) or
// the model test: the on-disk layout + Capacity surface, the distinct-
// artifacts-share-one-blob contract, the input-validation and inline-policy
// tables (category 6), and the Coalesced (WORM) identity mode. Content-
// addressing / round-trip / Walk visibility live in properties_test.go and
// model_test.go; pipeline round-trips in pipeline_test.go; encrypted Put in
// put_encrypted_test.go.

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// TestPut_OnDiskLayout: a fresh Put lands a manifest under manifests/,
// exactly one blob under blobs/, and Capacity reflects both. Pins the
// on-disk shape and the Capacity surface nothing else asserts.
func TestPut_OnDiskLayout(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), artifactfx.Payload("hello scrinium"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(string(id), "sha256-") {
		t.Errorf("ArtifactID prefix: got %q", id)
	}

	disk := storefx.OnDiskAt(root)
	digest := storekit.MustDigest(t, s, id)
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

// TestPut_SharedBlobAcrossArtifacts: identical content under two distinct
// SessionIDs produces two artifacts (distinct manifests/IDs) sharing a
// single on-disk blob, staging cleaned. This is the distinct-artifacts-
// share-one-blob case the content-addressing property does not cover.
func TestPut_SharedBlobAcrossArtifacts(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	const text = "shared content"

	id1, err := s.Put(context.Background(), artifactfx.Payload(text),
		domain.WithSession("a"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(context.Background(), artifactfx.Payload(text),
		domain.WithSession("b"))
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("different SessionID must produce different ArtifactIDs, got %q", id1)
	}

	disk := storefx.OnDiskAt(root)
	if n := disk.BlobCount(); n != 1 {
		t.Errorf("shared content should leave 1 blob, got %d", n)
	}
	if files := disk.StagingFiles(); len(files) > 0 {
		t.Errorf("staging directory not cleaned: %d entries", len(files))
	}

	var seen int
	if err := s.Walk(context.Background(), func(domain.Manifest) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Errorf("Walk returned %d manifests, want 2", seen)
	}
}

// TestPut_InputValidation: per-field rejection. Each case Puts one
// artifact and expects a specific sentinel (nil = any non-nil error).
func TestPut_InputValidation(t *testing.T) {
	huge := bytes.Repeat([]byte("a"), 64*1024+1)

	cases := []struct {
		name    string
		art     domain.Artifact
		opts    []domain.PutOption
		wantErr error // nil = any non-nil error
	}{
		{"session id too long", artifactfx.Payload("x"),
			[]domain.PutOption{domain.WithSession(domain.SessionID(strings.Repeat("a", 256)))}, errs.ErrSessionIDTooLong},
		{"ext too large",
			domain.Artifact{Payload: strings.NewReader("ok"), Ext: append([]byte(`"`), append(huge, '"')...)},
			nil, errs.ErrExtTooLarge},
		{"nil payload", domain.Artifact{Payload: nil}, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := storefx.InitWithRoot(t)
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

// TestPut_InlinePolicy: content at/under the limit stays inline (no blob
// file); over the limit it falls back to a Target blob; a zero limit
// disables inlining. The final sub-test pins that inline manifests do not
// dedup (two identical inline Puts → zero blob files, two manifests).
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
			s, root := storefx.InitInline(t, tc.limit)
			id, err := s.Put(context.Background(), artifactfx.Payload(tc.content))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			disk := storefx.OnDiskAt(root)
			if n := disk.BlobCount(); n != tc.wantBlobs {
				t.Errorf("blob files: got %d, want %d", n, tc.wantBlobs)
			}
			m := disk.ReadManifest(t, storekit.MustDigest(t, s, id))
			if m.LayoutHeader.BlobStorage != tc.wantLayout {
				t.Errorf("LayoutHeader: got %q, want %q", m.LayoutHeader.BlobStorage, tc.wantLayout)
			}
			if m.OriginalSize != int64(len(tc.content)) {
				t.Errorf("OriginalSize: got %d, want %d", m.OriginalSize, len(tc.content))
			}
		})
	}

	t.Run("no dedup for inline", func(t *testing.T) {
		s, root := storefx.InitInline(t, 100)
		const content = "shared inline"
		for _, sid := range []domain.SessionID{"a", "b"} {
			if _, err := s.Put(context.Background(), artifactfx.Payload(content),
				domain.WithSession(sid)); err != nil {
				t.Fatal(err)
			}
		}
		if n := storefx.OnDiskAt(root).BlobCount(); n != 0 {
			t.Errorf("blob files after 2 inline Puts: got %d, want 0", n)
		}
		var manifests int
		if err := s.Walk(context.Background(), func(domain.Manifest) error {
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

// TestPut_CoalescedMode_SameContentCoalesces: in IdentityModeCoalesced the
// per-Put nonce is omitted (ADR-73), so two Puts of identical
// content+identity derive the same handle and the second collapses into
// the first (ON CONFLICT DO NOTHING) — one artifact, one blob. The
// WORM-archive contract that distinguishes Coalesced from the Unique
// default (where the two Puts are distinct artifacts).
func TestPut_CoalescedMode_SameContentCoalesces(t *testing.T) {
	s := storefx.Init(t, store.WithCoalesced())
	ctx := context.Background()

	id1, err := s.Put(ctx, artifactfx.Payload("identical bytes"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(ctx, artifactfx.Payload("identical bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("Coalesced: identical content must yield one handle, got %q vs %q", id1, id2)
	}

	info, err := s.Capacity(ctx)
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 1 {
		t.Errorf("artifact count: got %d, want 1 (coalesced)", info.ArtifactCount)
	}
	if info.BlobCount != 1 {
		t.Errorf("blob count: got %d, want 1", info.BlobCount)
	}

	var seen int
	if err := s.Walk(ctx, func(domain.Manifest) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("Walk: got %d manifests, want 1 (coalesced)", seen)
	}
}

// TestPut_CoalescedMode_DistinctContentStaysDistinct: coalescing keys on
// content — different content under the same identity stays two artifacts
// even in Coalesced mode.
func TestPut_CoalescedMode_DistinctContentStaysDistinct(t *testing.T) {
	s := storefx.Init(t, store.WithCoalesced())
	ctx := context.Background()

	idA, err := s.Put(ctx, artifactfx.Payload("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.Put(ctx, artifactfx.Payload("beta"))
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Fatalf("distinct content must not coalesce, both = %q", idA)
	}

	info, err := s.Capacity(ctx)
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 2 {
		t.Errorf("artifact count: got %d, want 2", info.ArtifactCount)
	}
}

// TestPut_LargePayload: a 1 MiB payload streams through and its manifest
// reports the correct id and OriginalSize.
func TestPut_LargePayload(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	const N = 1 << 20 // 1 MiB
	data := bytes.Repeat([]byte{0xab}, N)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("Put 1MiB: %v", err)
	}

	var seen domain.Manifest
	if err := s.Walk(context.Background(), func(m domain.Manifest) error {
		seen = m
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen.ArtifactID != id {
		t.Errorf("walked manifest ID: got %q, want %q", seen.ArtifactID, id)
	}
	if seen.OriginalSize != int64(N) {
		t.Errorf("OriginalSize: got %d, want %d", seen.OriginalSize, N)
	}
}

// TestPut_DefaultNamespace: an empty Namespace lands in the default and is
// visible via Walk.
func TestPut_DefaultNamespace(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), artifactfx.Payload("default ns"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	var seen int
	_ = s.Walk(context.Background(), func(m domain.Manifest) error {
		seen++
		return nil
	})
	if seen != 1 {
		t.Errorf("default ns walk: got %d, want 1", seen)
	}
}

// TestPut_BlobTypeOtherThanRegular_Deferred: a non-Regular BlobType
// (Chunk) is refused with an error referencing the deferring milestone.
func TestPut_BlobTypeOtherThanRegular_Deferred(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Put(context.Background(),
		artifactfx.Payload("nope"),
		domain.WithBlobType(domain.BlobTypeChunk))
	if err == nil {
		t.Fatal("expected error on Chunk BlobType")
	}
	if !strings.Contains(err.Error(), "M3") {
		t.Errorf("error should reference M3: %v", err)
	}
}

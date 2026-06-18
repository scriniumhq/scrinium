// Collapsed Delete tests. Delete-then-NotFound and Walk-removal are
// exercised by the model test; retention, state/policy guards,
// not-found / empty-id and double-delete moved to the cross-operation
// tables. What remains here is the logical-delete on-disk contract
// (manifest gone, blob retained for GC) for both layouts, and the
// shared-blob ref-count lifecycle.
//
// Replaces the previous store_delete_test.go in full.

package store_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	storefx2 "scrinium.dev/testutil/storefx"
)

// TestDelete_RemovesManifest: a logical delete removes the manifest
// (file gone, Walk empty, Get NotFound) for both Target and Inline
// layouts. For Target the physical blob is retained — physical removal
// is the GC agent's job. The Inline case also guards the regression
// where an inline manifest (no manifest_blobs edges) was left in the
// index after delete.
func TestDelete_RemovesManifest(t *testing.T) {
	cases := []struct {
		name   string
		inline bool
	}{
		{"target", false},
		{"inline", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				s    store.Store
				root string
			)
			if tc.inline {
				s, root = newInlineStore(t, 100)
			} else {
				s, root = storefx2.InitWithRoot(t)
			}
			id, err := s.Put(context.Background(), payload("delete me"))
			if err != nil {
				t.Fatal(err)
			}
			// Capture the on-disk digest (the manifest filename) before the
			// delete removes the artifact — afterwards the handle no longer
			// resolves.
			digest := mustDigest(t, s, id)
			if err := s.Delete(context.Background(), id); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			disk := storefx2.OnDiskAt(root)
			if disk.ManifestExists(digest) {
				t.Errorf("manifest file should be gone")
			}

			var seen int
			if err := s.Walk(context.Background(), func(domain.Manifest) error {
				seen++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if seen != 0 {
				t.Errorf("Walk after delete: got %d, want 0", seen)
			}

			if _, err := s.Get(context.Background(), id); !errors.Is(err, errs.ErrArtifactNotFound) {
				t.Errorf("Get after delete: got %v, want errs.ErrArtifactNotFound", err)
			}

			// Target: the blob lingers (physical GC is deferred). Inline:
			// there was never a blob file.
			wantBlobs := 1
			if tc.inline {
				wantBlobs = 0
			}
			if n := disk.BlobCount(); n != wantBlobs {
				t.Errorf("blob count after logical delete: got %d, want %d", n, wantBlobs)
			}
		})
	}
}

// TestStore_RefCountLifecycle: two artifacts sharing one blob — deleting
// one keeps the other readable and the shared blob on disk (ref_count
// drops 2→1, not to zero). The refcount itself is not visible through
// the public API, so this is the store-level contract the model test
// (which checks content/Walk/blob-count, not refcounts) does not cover.
func TestStore_RefCountLifecycle(t *testing.T) {
	s, root := storefx2.InitWithRoot(t)
	const text = "shared content for delete"

	idA, err := s.Put(context.Background(), payload(text),
		domain.WithSession("a"))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.Put(context.Background(), payload(text),
		domain.WithSession("b"))
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Fatalf("shared-blob setup broken: ids equal")
	}
	if n := storefx2.OnDiskAt(root).BlobCount(); n != 1 {
		t.Fatalf("two artifacts should share 1 blob, got %d", n)
	}

	if err := s.Delete(context.Background(), idA); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	if _, err := s.Get(context.Background(), idA); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get(A) after delete: got %v, want errs.ErrArtifactNotFound", err)
	}
	rh, err := s.Get(context.Background(), idB)
	if err != nil {
		t.Fatalf("Get(B) after deleting A: %v", err)
	}
	got, _ := io.ReadAll(rh)
	rh.Close()
	if string(got) != text {
		t.Errorf("B payload after deleting A: got %q, want %q", got, text)
	}

	if n := storefx2.OnDiskAt(root).BlobCount(); n != 1 {
		t.Errorf("shared blob should remain after deleting one referrer: got %d, want 1", n)
	}
}

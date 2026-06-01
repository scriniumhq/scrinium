package indextest

import (
	"context"
	"io/fs"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/testutil/manifestfx"
)

// --- ListOrphanBlobs ---

func runListOrphanBlobs(t *testing.T, f Factory) {
	// Reaching ref_count=0 through the public API: IndexManifest
	// then DeleteManifest. The blob row remains as an orphan —
	// that is the state ListOrphanBlobs reports.

	t.Run("Basic", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		// Two live blobs, two orphans.
		stage := []struct {
			id, ref  string
			fillChar byte
			deleted  bool
		}{
			{"live-1", "blob-l1", 'a', false},
			{"orph-1", "blob-o1", 'b', true},
			{"orph-2", "blob-o2", 'c', true},
			{"live-2", "blob-l2", 'd', false},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
			if s.deleted {
				if err := idx.DeleteManifest(ctx, domain.ArtifactID(s.id), []string{s.ref}); err != nil {
					t.Fatalf("delete %s: %v", s.id, err)
				}
			}
		}

		var got []string
		err := idx.ListOrphanBlobs(context.Background(), func(ref string) error {
			got = append(got, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListOrphanBlobs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d, want 2", len(got))
		}
		seen := make(map[string]bool)
		for _, ref := range got {
			seen[ref] = true
		}
		if !seen["blob-o1"] || !seen["blob-o2"] {
			t.Errorf("expected both orphans, got %v", got)
		}
	})

	t.Run("StopWalk", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		for i := 0; i < 5; i++ {
			fillChar := byte('a' + i)
			id := "art-" + string(fillChar)
			ref := "blob-" + string(fillChar)
			m := manifestfx.BlobWithHash(id, ref, manifestfx.SyntheticHash(fillChar), 1024)
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+ref), nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := idx.DeleteManifest(ctx, domain.ArtifactID(id), []string{ref}); err != nil {
				t.Fatal(err)
			}
		}

		var seen int
		err := idx.ListOrphanBlobs(context.Background(), func(ref string) error {
			seen++
			if seen == 2 {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			t.Fatalf("fs.SkipAll must be swallowed, got %v", err)
		}
		if seen != 2 {
			t.Fatalf("expected stop at 2, saw %d", seen)
		}
	})
}

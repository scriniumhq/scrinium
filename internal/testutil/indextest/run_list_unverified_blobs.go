package indextest

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/internal/testutil/manifestfx"
)

// --- ListUnverifiedBlobs ---

func runListUnverifiedBlobs(t *testing.T, f Factory) {
	// IndexManifest creates blobs with no verification timestamp;
	// MarkVerified sets it. The iterator surfaces blobs whose
	// last verification (or absence thereof) places them before
	// the cutoff — never-verified rows always qualify, recently
	// verified rows are skipped.

	t.Run("CutoffBoundary", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		stage := []struct {
			id, ref      string
			fillChar     byte
			verifiedAgo  time.Duration
			everVerified bool
		}{
			{"never", "blob-n", 'a', 0, false},
			{"stale", "blob-s", 'b', 10 * time.Minute, true},
			{"fresh", "blob-f", 'c', time.Minute, true},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
			if s.everVerified {
				if err := idx.MarkVerified(ctx, s.ref, now.Add(-s.verifiedAgo)); err != nil {
					t.Fatalf("MarkVerified %s: %v", s.ref, err)
				}
			}
		}

		cutoff := now.Add(-5 * time.Minute)
		var got []string
		err := idx.ListUnverifiedBlobs(context.Background(), cutoff, func(ref string) error {
			got = append(got, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListUnverifiedBlobs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d, want 2 (never+stale)", len(got))
		}
		seen := make(map[string]bool)
		for _, ref := range got {
			seen[ref] = true
		}
		if !seen["blob-n"] {
			t.Error("expected never-verified blob in result")
		}
		if !seen["blob-s"] {
			t.Error("expected stale blob in result")
		}
		if seen["blob-f"] {
			t.Error("fresh blob leaked through cutoff")
		}
	})

	t.Run("OldestFirst", func(t *testing.T) {
		ctx := t.Context()
		// Sorting order: oldest verification first. NEVER-verified
		// rows are also reported, but the relative position of
		// NEVER vs verified rows is implementation-defined when
		// last_verified_at is treated as a NULL/sentinel value;
		// this test pins down the pure-time ordering between
		// rows that have a verification timestamp.
		idx := f.New(t)
		now := time.Now().UTC().Truncate(time.Second)

		stage := []struct {
			id, ref     string
			fillChar    byte
			verifiedAgo time.Duration
		}{
			{"older", "blob-o", 'a', 3 * time.Hour},
			{"middle", "blob-m", 'b', 2 * time.Hour},
			{"newer", "blob-n", 'c', time.Hour},
		}
		for _, s := range stage {
			m := manifestfx.BlobWithHash(s.id, s.ref, manifestfx.SyntheticHash(s.fillChar), 1024)
			if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p/"+s.ref), nil, nil); err != nil {
				t.Fatalf("seed %s: %v", s.id, err)
			}
			if err := idx.MarkVerified(ctx, s.ref, now.Add(-s.verifiedAgo)); err != nil {
				t.Fatalf("MarkVerified %s: %v", s.ref, err)
			}
		}

		cutoff := now
		var got []string
		err := idx.ListUnverifiedBlobs(context.Background(), cutoff, func(ref string) error {
			got = append(got, ref)
			return nil
		})
		if err != nil {
			t.Fatalf("ListUnverifiedBlobs: %v", err)
		}
		want := []string{"blob-o", "blob-m", "blob-n"}
		if len(got) != len(want) {
			t.Fatalf("got %d, want %d", len(got), len(want))
		}
		for i, ref := range got {
			if ref != want[i] {
				t.Errorf("position %d: got %q, want %q", i, ref, want[i])
			}
		}
	})
}

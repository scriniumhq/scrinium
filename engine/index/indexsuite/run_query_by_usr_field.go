package indexsuite

import (
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/testutil/manifestfx"
)

// --- QueryByUsrField ---

// QueryByUsrField is gated by the in-memory usr_indexing switch
// (index.UsrIndexingSwitch), which is read at index time and defaults to off. The switch must be on BEFORE
// IndexManifest for the usr projection to be written; flipping it after the
// fact does not backfill. With the switch off the query returns an empty
// result and not an error.
func runQueryByUsrField(t *testing.T, f Factory) {
	t.Run("IndexingOn_YieldsArtifact", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		registerFixture(t, ctx, idx)

		sw, ok := idx.(index.UsrIndexingSwitch)
		if !ok {
			t.Skip("index does not support usr indexing")
		}
		sw.SetUsrIndexing(true)
		if err := idx.IndexManifest(ctx, manifestfx.Blob("art-1", "blob-1"), manifestfx.PhysAddr("blobs/aa/bb/blob-1")); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		var got []domain.ArtifactID
		err := idx.QueryByUsrField(ctx, fixtureUsrField, fixtureUsrValue, func(id domain.ArtifactID) error {
			got = append(got, id)
			return nil
		})
		if err != nil {
			t.Fatalf("QueryByUsrField: %v", err)
		}
		if len(got) != 1 || got[0] != "art-1" {
			t.Errorf("QueryByUsrField(%q, %q): got %v, want [art-1]", fixtureUsrField, fixtureUsrValue, got)
		}
	})

	t.Run("IndexingOff_YieldsNothing", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		registerFixture(t, ctx, idx)

		// usr_indexing left at its default (off): the usr projection is
		// never written, so the query yields nothing — and not an error.
		if err := idx.IndexManifest(ctx, manifestfx.Blob("art-1", "blob-1"), manifestfx.PhysAddr("p")); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		var got []domain.ArtifactID
		err := idx.QueryByUsrField(ctx, fixtureUsrField, fixtureUsrValue, func(id domain.ArtifactID) error {
			got = append(got, id)
			return nil
		})
		if err != nil {
			t.Fatalf("QueryByUsrField (indexing off): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("QueryByUsrField with usr_indexing off: got %v, want none", got)
		}
	})
}

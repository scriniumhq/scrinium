package drivertest

import "testing"

func runPruneEmptyDirs(t *testing.T, f Factory) {
	// A missing prefix is a no-op, never an error.
	t.Run("MissingPrefixIsNoOp", func(t *testing.T) {
		d := f.New(t)
		if err := d.PruneEmptyDirs(t.Context(), "no/such/path"); err != nil {
			t.Fatalf("expected no-op, got %v", err)
		}
	})

	// Pruning must never disturb live objects. On object stores with no
	// directory concept this is a no-op; the objects must still be there.
	t.Run("PreservesLiveObjects", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "kept/file", "x")
		if err := d.PruneEmptyDirs(ctx, ""); err != nil {
			t.Fatal(err)
		}
		if got := getBlob(t, d, "kept/file"); got != "x" {
			t.Fatalf("live object disturbed by prune: got %q", got)
		}
	})
}

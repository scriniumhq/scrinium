package drivertest

import "testing"

// The tombstone suite checks the lifecycle purely through the interface
// (MarkTombstone, IsTombstone, TombstoneInfo, RemoveTombstone). Whether
// the original object stays visible after marking is backend-specific
// (localfs renames it away; an object store may keep it under a tag), so
// that is asserted in the backend's own tests, not here.
func runTombstone(t *testing.T, f Factory) {
	// Marking an existing object sets its tombstone.
	t.Run("MarkSetsTombstone", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "data")
		if err := d.MarkTombstone(ctx, "f"); err != nil {
			t.Fatal(err)
		}
		on, err := d.IsTombstone(ctx, "f")
		if err != nil {
			t.Fatal(err)
		}
		if !on {
			t.Fatal("expected tombstone after MarkTombstone")
		}
	})

	// Marking a path that was never written is valid: in multi-host
	// setups the deletion decision can arrive before the local replica.
	t.Run("MarkNeverWritten", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		if err := d.MarkTombstone(ctx, "never_existed"); err != nil {
			t.Fatal(err)
		}
		on, err := d.IsTombstone(ctx, "never_existed")
		if err != nil {
			t.Fatal(err)
		}
		if !on {
			t.Fatal("expected tombstone for a never-written path")
		}
	})

	// Repeated marking is idempotent.
	t.Run("Idempotent", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "data")
		for i := 0; i < 3; i++ {
			if err := d.MarkTombstone(ctx, "f"); err != nil {
				t.Fatalf("iter %d: %v", i, err)
			}
		}
		on, err := d.IsTombstone(ctx, "f")
		if err != nil {
			t.Fatal(err)
		}
		if !on {
			t.Fatal("expected tombstone to remain after repeated marks")
		}
	})

	// A live, unmarked object is not a tombstone.
	t.Run("FalseForUntombstoned", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "data")
		on, err := d.IsTombstone(ctx, "f")
		if err != nil {
			t.Fatal(err)
		}
		if on {
			t.Fatal("unmarked object reported as tombstone")
		}
	})

	// TombstoneInfo reports absence before marking and a non-zero mark
	// time afterwards (the GC grace period is measured from it).
	t.Run("InfoReportsMarkTime", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "data")

		if marked, _, err := d.TombstoneInfo(ctx, "f"); err != nil {
			t.Fatal(err)
		} else if marked {
			t.Fatal("TombstoneInfo reported marked before MarkTombstone")
		}

		if err := d.MarkTombstone(ctx, "f"); err != nil {
			t.Fatal(err)
		}
		marked, since, err := d.TombstoneInfo(ctx, "f")
		if err != nil {
			t.Fatal(err)
		}
		if !marked {
			t.Fatal("TombstoneInfo reported absent after MarkTombstone")
		}
		if since.IsZero() {
			t.Fatal("TombstoneInfo returned a zero mark time for a set tombstone")
		}
	})

	// RemoveTombstone clears the marker; afterwards IsTombstone is false,
	// and removing an already-absent marker is a no-op.
	t.Run("RemoveClearsTombstone", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "data")
		if err := d.MarkTombstone(ctx, "f"); err != nil {
			t.Fatal(err)
		}
		if err := d.RemoveTombstone(ctx, "f"); err != nil {
			t.Fatal(err)
		}
		on, err := d.IsTombstone(ctx, "f")
		if err != nil {
			t.Fatal(err)
		}
		if on {
			t.Fatal("tombstone still present after RemoveTombstone")
		}
		if err := d.RemoveTombstone(ctx, "f"); err != nil {
			t.Fatalf("RemoveTombstone (missing) should be a no-op, got %v", err)
		}
	})
}

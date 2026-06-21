package drivertest

import (
	"errors"
	"os"
	"testing"
)

func runRemove(t *testing.T, f Factory) {
	// Remove of an existing object succeeds; Remove of a missing object
	// is a no-op, so deletion is idempotent.
	t.Run("Idempotent", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "x")
		if err := d.Remove(ctx, "f"); err != nil {
			t.Fatalf("Remove (existing): %v", err)
		}
		if err := d.Remove(ctx, "f"); err != nil {
			t.Fatalf("Remove (missing): %v", err)
		}
	})

	// After Remove the object is gone.
	t.Run("RemovedIsGone", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "f", "x")
		if err := d.Remove(ctx, "f"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Get(ctx, "f"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected os.ErrNotExist after Remove, got %v", err)
		}
	})
}

package drivertest

import (
	"errors"
	"os"
	"testing"
)

func runRename(t *testing.T, f Factory) {
	// Rename moves the object to the destination key (creating any
	// intermediate structure) and leaves nothing at the source.
	t.Run("MovesObject", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "src", "data")
		if err := d.Rename(ctx, "src", "deeper/dst"); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if got := getBlob(t, d, "deeper/dst"); got != "data" {
			t.Fatalf("dst content: got %q, want %q", got, "data")
		}
		if _, err := d.Stat(ctx, "src"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("src still exists after rename: %v", err)
		}
	})
}

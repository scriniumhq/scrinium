package drivertest

import (
	"errors"
	"os"
	"testing"
)

func runStat(t *testing.T, f Factory) {
	// Stat of an existing object reports its size, file-ness, and a
	// non-zero modification time.
	t.Run("ReturnsMetadata", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "f", "hello")
		info, err := d.Stat(t.Context(), "f")
		if err != nil {
			t.Fatal(err)
		}
		if info.Size != 5 {
			t.Errorf("size: got %d, want 5", info.Size)
		}
		if info.IsDir {
			t.Error("expected file, got dir")
		}
		if info.ModTime.IsZero() {
			t.Error("ModTime not set")
		}
	})

	// Stat of a missing object reports os.ErrNotExist.
	t.Run("NotFound", func(t *testing.T) {
		d := f.New(t)
		if _, err := d.Stat(t.Context(), "missing"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected os.ErrNotExist, got %v", err)
		}
	})
}

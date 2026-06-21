package drivertest

import (
	"errors"
	"os"
	"testing"
)

func runGet(t *testing.T, f Factory) {
	// Get of a missing object reports os.ErrNotExist.
	t.Run("NotFound", func(t *testing.T) {
		d := f.New(t)
		_, err := d.Get(t.Context(), "missing")
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected os.ErrNotExist, got %v", err)
		}
	})
}

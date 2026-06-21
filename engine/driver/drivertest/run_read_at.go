package drivertest

import (
	"bytes"
	"io"
	"testing"
)

func runReadAt(t *testing.T, f Factory) {
	// A bounded range returns exactly the requested window.
	t.Run("Range", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "f", "0123456789abcdef")
		r, err := d.ReadAt(t.Context(), "f", 4, 6)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, _ := io.ReadAll(r)
		if !bytes.Equal(got, []byte("456789")) {
			t.Fatalf("got %q, want %q", got, "456789")
		}
	})

	// A range extending past EOF is clamped to the available bytes.
	t.Run("PastEnd", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "f", "abc")
		r, err := d.ReadAt(t.Context(), "f", 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, _ := io.ReadAll(r)
		if string(got) != "bc" {
			t.Fatalf("got %q, want %q", got, "bc")
		}
	})

	// A negative offset is rejected.
	t.Run("NegativeOffset", func(t *testing.T) {
		d := f.New(t)
		if _, err := d.ReadAt(t.Context(), "f", -1, 1); err == nil {
			t.Fatal("expected error on negative offset")
		}
	})
}

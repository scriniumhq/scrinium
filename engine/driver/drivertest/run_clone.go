package drivertest

import "testing"

func runClone(t *testing.T, f Factory) {
	// Clone copies the source object to the destination and leaves the
	// source intact (an independent copy).
	t.Run("CopiesObject", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()
		putBlob(t, d, "src/file", "payload")
		if err := d.Clone(ctx, "src/file", "dst/copy"); err != nil {
			t.Fatal(err)
		}
		if got := getBlob(t, d, "dst/copy"); got != "payload" {
			t.Fatalf("clone content: got %q, want %q", got, "payload")
		}
		if _, err := d.Stat(ctx, "src/file"); err != nil {
			t.Fatalf("source disturbed by clone: %v", err)
		}
	})
}

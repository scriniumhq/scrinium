package drivertest

import (
	"path"
	"testing"
)

func runList(t *testing.T, f Factory) {
	// List reports the objects under a prefix. The exact string form is
	// backend-defined (localfs returns root-relative slash paths), so we
	// compare by basename to stay implementation-neutral.
	t.Run("ReportsChildren", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "p/alpha", "1")
		putBlob(t, d, "p/beta", "2")

		entries, err := d.List(t.Context(), "p")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got := make(map[string]bool, len(entries))
		for _, e := range entries {
			got[path.Base(e)] = true
		}
		for _, want := range []string{"alpha", "beta"} {
			if !got[want] {
				t.Errorf("List(%q) missing %q; got %v", "p", want, entries)
			}
		}
	})
}

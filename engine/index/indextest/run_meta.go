package indextest

import "testing"

// --- GetMeta / SetMeta ---

// The StoreIndex contract for store_meta is a singleton key/value store:
// SetMeta writes (or overwrites) and GetMeta reads back. The missing-key
// outcome is left to the backend (sqlite returns errs.ErrMetaKeyNotFound,
// covered in its own suite) and is intentionally not asserted here.
func runMeta(t *testing.T, f Factory) {
	t.Run("RoundTrip", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)

		const key, value = "indextest.meta.key", "indextest.meta.value"
		if err := idx.SetMeta(ctx, key, value); err != nil {
			t.Fatalf("SetMeta: %v", err)
		}
		got, err := idx.GetMeta(ctx, key)
		if err != nil {
			t.Fatalf("GetMeta: %v", err)
		}
		if got != value {
			t.Errorf("GetMeta(%q): got %q, want %q", key, got, value)
		}
	})

	t.Run("Overwrite", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)

		const key = "indextest.meta.key"
		if err := idx.SetMeta(ctx, key, "first"); err != nil {
			t.Fatalf("SetMeta first: %v", err)
		}
		if err := idx.SetMeta(ctx, key, "second"); err != nil {
			t.Fatalf("SetMeta second: %v", err)
		}
		got, err := idx.GetMeta(ctx, key)
		if err != nil {
			t.Fatalf("GetMeta: %v", err)
		}
		if got != "second" {
			t.Errorf("GetMeta after overwrite: got %q, want %q", got, "second")
		}
	})
}

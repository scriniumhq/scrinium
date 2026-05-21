package storefx

import (
	"context"
	"testing"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/store"
)

// Reopener captures the (Driver, StoreIndex) pair so a test can
// reopen the same Location across the engine boundary. drv and idx
// outlive any individual store.Store; the Reopener exists for
// scenarios where the test sequence is "Init → close store →
// reopen with different options" — common in encrypted-Store
// flows where the second open uses AutoUnlock or a new passphrase.
//
// Reopener does NOT register cleanup for drv/idx — those are
// already cleaned up by the underlying driverfx / indexfx fixtures
// against t.
type Reopener struct {
	drv driver.Driver
	idx store.StoreIndex
}

// Driver returns the underlying driver. Tests use this for direct
// on-disk inspection (descriptor.Read, raw drv.Get on a known path).
func (r *Reopener) Driver() driver.Driver { return r.drv }

// Index returns the captured StoreIndex.
func (r *Reopener) Index() store.StoreIndex { return r.idx }

// Root returns the localfs root if the underlying driver is a
// localfs.Driver; empty string otherwise. Used by tests that need
// to walk the on-disk tree directly.
func (r *Reopener) Root() string {
	if d, ok := r.drv.(*localfs.Driver); ok {
		return d.Root()
	}
	return ""
}

// Open reopens the captured Location. WithStoreIndex and
// WithHashRegistry are filled in automatically; pass any other
// option (WithPassphrase, WithAutoUnlock, WithConfig, ...) through
// extra. Calls t.Fatalf on failure.
func (r *Reopener) Open(t testing.TB, extra ...store.StoreOption) store.Store {
	t.Helper()
	opts := append([]store.StoreOption{
		store.WithStoreIndex(r.idx),
		store.WithHashRegistry(Hashes()),
	}, extra...)
	s, err := store.OpenStore(context.Background(), r.drv, opts...)
	if err != nil {
		t.Fatalf("storefx.Reopener.Open: %v", err)
	}
	return s
}

// TryOpen is the non-fatal variant of Open: it returns the error
// instead of t.Fatalf. Use when the test's assertion is on the
// failure mode itself (wrong passphrase, ConfigMismatch, ...).
func (r *Reopener) TryOpen(t testing.TB, extra ...store.StoreOption) (store.Store, error) {
	t.Helper()
	opts := append([]store.StoreOption{
		store.WithStoreIndex(r.idx),
		store.WithHashRegistry(Hashes()),
	}, extra...)
	return store.OpenStore(context.Background(), r.drv, opts...)
}

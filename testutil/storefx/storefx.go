// Package storefx supplies Store fixtures for tests.
package storefx

import (
	"context"
	"crypto/sha256"
	"hash"
	"path/filepath"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/systemstore"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
)

// Hashes returns a HashRegistry suitable for tests. sha256 is real;
// blake3 is registered as an sha256-backed stub so tests that set
// ContentHasher: HashBLAKE3 do not need to pull in a blake3 library.
// Tests that care about a specific algorithm register their own.
func Hashes() domain.HashRegistry {
	return hashing.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() }).
		Register("blake3", func() hash.Hash { return sha256.New() })
}

// Init: fresh Store on localfs + in-memory sqlite index + sha256.
// Caller opts append to (and can override) defaults.
func Init(t testing.TB, opts ...store.StoreOption) store.Store {
	t.Helper()
	all := append([]store.StoreOption{store.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, driverfx.LocalFS(t), all...)
	return s
}

// InitWithRoot is Init plus the driver root for on-disk inspection.
func InitWithRoot(t testing.TB, opts ...store.StoreOption) (store.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	all := append([]store.StoreOption{store.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, drv, all...)
	return s, drv.Root()
}

// InitShared bootstraps a fresh Plain Store and returns the pieces an
// agent test needs to drive maintenance against the SAME backend the
// Store uses: the Store, the concrete localfs driver (for its Root and
// to hand to an agent), and the shared in-memory index. A maintenance
// agent must observe the very rows the Store wrote, so it has to share
// this index handle rather than open its own.
func InitShared(t testing.TB, opts ...store.StoreOption) (store.Store, *localfs.Driver, index.StoreIndex) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	all := append([]store.StoreOption{store.WithStoreIndex(idx)}, opts...)
	s, _ := initStore(t, drv, all...)
	return s, drv, idx
}

// InitOn wires Init around a caller-provided driver. Caller also
// owns the index — pass store.WithStoreIndex explicitly.
func InitOn(t testing.TB, drv driver.Driver, opts ...store.StoreOption) store.Store {
	t.Helper()
	s, _ := initStore(t, drv, opts...)
	return s
}

// InitEncryptedOn bootstraps an encrypted Store on a caller-provided
// driver. A fresh in-memory index is wired up internally, then
// discarded — the caller's intent with this helper is to seed an
// on-disk Location and immediately exercise OpenStore against it
// (e.g. to verify the full bootstrap path on a deliberately
// damaged Location). For tests that want to keep using the same
// (drv, idx) pair across init+reopen, see InitEncrypted +
// Reopener.Open.
func InitEncryptedOn(t testing.TB, drv driver.Driver, pass string, extra ...store.StoreOption) {
	t.Helper()
	opts := append([]store.StoreOption{
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithPassphrase(StaticPP(pass)),
	}, extra...)
	if _, _, err := store.InitStore(context.Background(), drv, append([]store.StoreOption{store.WithHashRegistry(Hashes())}, opts...)...); err != nil {
		t.Fatalf("storefx.InitEncryptedOn: %v", err)
	}
}

// InitPlainOn is the Plain-DEK counterpart of InitEncryptedOn:
// bootstrap on a caller-supplied driver with a discarded index, no
// passphrase. Use to seed a Location for subsequent OpenStore
// scenarios that do not need a Reopener.
func InitPlainOn(t testing.TB, drv driver.Driver, extra ...store.StoreOption) {
	t.Helper()
	opts := append([]store.StoreOption{store.WithStoreIndex(indexfx.Memory(t))}, extra...)
	if _, _, err := store.InitStore(context.Background(), drv, append([]store.StoreOption{store.WithHashRegistry(Hashes())}, opts...)...); err != nil {
		t.Fatalf("storefx.InitPlainOn: %v", err)
	}
}

// TryOpenOn calls store.OpenStore on a caller-supplied driver with
// a fresh in-memory index and the standard test hash registry.
// Returns the (Store, error) pair so the caller can assert on the
// failure mode itself — fatal-on-failure callers should use OpenOn.
func TryOpenOn(t testing.TB, drv driver.Driver, extra ...store.StoreOption) (store.Store, error) {
	t.Helper()
	opts := append([]store.StoreOption{
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(Hashes()),
	}, extra...)
	return store.OpenStore(context.Background(), drv, opts...)
}

// OpenOn is the fatal variant of TryOpenOn: it calls t.Fatalf when
// OpenStore returns an error and returns just the Store on success.
func OpenOn(t testing.TB, drv driver.Driver, extra ...store.StoreOption) store.Store {
	t.Helper()
	s, err := TryOpenOn(t, drv, extra...)
	if err != nil {
		t.Fatalf("storefx.OpenOn: %v", err)
	}
	return s
}

func initStore(t testing.TB, drv driver.Driver, opts ...store.StoreOption) (store.Store, []byte) {
	t.Helper()
	all := append([]store.StoreOption{store.WithHashRegistry(Hashes())}, opts...)
	s, kit, err := store.InitStore(context.Background(), drv, all...)
	if err != nil {
		t.Fatalf("storefx.Init: %v", err)
	}
	return s, kit
}

// InitPlain bootstraps a fresh Plain Store and returns a Reopener
// bound to the same (drv, idx) so the test can reopen the Location
// later. Use whenever the test exercises a reopen-flow; for
// single-Init tests, plain Init/InitWithRoot remain shorter.
func InitPlain(t testing.TB, extra ...store.StoreOption) (store.Store, *Reopener) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	opts := append([]store.StoreOption{store.WithStoreIndex(idx)}, extra...)
	s, _ := initStore(t, drv, opts...)
	return s, &Reopener{drv: drv, idx: idx}
}

// InitEncrypted bootstraps a fresh encrypted Store with the given
// passphrase and returns a Reopener. The Store is fully Unlocked
// after Init (per the Plain-DEK→Encrypted transition described in
// core/lifecycle.go's InitStore).
//
// extra options are appended to the engine's defaults; pass
// store.WithKDFParams or store.WithConfig as needed.
func InitEncrypted(t testing.TB, pass string, extra ...store.StoreOption) (store.Store, *Reopener) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	opts := append([]store.StoreOption{
		store.WithStoreIndex(idx),
		store.WithPassphrase(StaticPP(pass)),
	}, extra...)
	s, _ := initStore(t, drv, opts...)
	return s, &Reopener{drv: drv, idx: idx}
}

// InitEncryptedLocked bootstraps an encrypted Store and reopens it
// WITHOUT AutoUnlock so the returned Store is in StateLocked. The
// PassphraseProvider configured on the second open uses the same
// passphrase — tests that need a different provider can replace it
// via Reopener.Open with WithPassphrase override.
func InitEncryptedLocked(t testing.TB, pass string, extra ...store.StoreOption) (store.Store, *Reopener) {
	t.Helper()
	_, r := InitEncrypted(t, pass, extra...)
	s := r.Open(t,
		store.WithPassphrase(StaticPP(pass)),
	)
	return s, r
}

// InitInline initialises a Plain Store backed by inline blob storage with
// the given inline-blob limit, returning the Store and its root.
func InitInline(t testing.TB, limit int64) (store.Store, string) {
	t.Helper()
	cfg := domain.StoreConfig{
		BlobStorage:     domain.BlobStorageInline,
		InlineBlobLimit: limit,
	}
	return InitWithRoot(t, store.WithConfig(cfg))
}

// InitDisk initialises a Plain Store on a fresh localfs driver with a
// disk-backed index, returning the Store and the driver root. Use it for
// tests that reopen the Store and need on-disk persistence of both the
// blobs and the index.
func InitDisk(t testing.TB) (store.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	root := drv.Root()
	idx := indexfx.Disk(t, filepath.Join(t.TempDir(), "store.idx"))
	s := InitOn(t, drv, store.WithStoreIndex(idx))
	return s, root
}

// InitPlainSystem initialises a Plain Store and returns its system plane
// plus the backing StoreIndex. The Store is closed on cleanup.
func InitPlainSystem(t testing.TB) (systemstore.Store, index.StoreIndex) {
	t.Helper()
	s, r := InitPlain(t)
	t.Cleanup(func() { _ = s.Close() })
	return s.System(), r.Index()
}

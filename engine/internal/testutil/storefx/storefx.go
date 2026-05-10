// Package storefx supplies Store fixtures for tests.
package storefx

import (
	"context"
	"crypto/sha256"
	"hash"
	"strings"
	"testing"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// Hashes returns a HashRegistry suitable for tests. sha256 is real;
// blake3 is registered as an sha256-backed stub so tests that set
// ContentHasher: HashBLAKE3 do not need to pull in a blake3 library.
// Tests that care about a specific algorithm register their own.
func Hashes() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() }).
		Register("blake3", func() hash.Hash { return sha256.New() })
}

// Init: fresh Store on localfs + in-memory sqlite index + sha256.
// Caller opts append to (and can override) defaults.
func Init(t testing.TB, opts ...core.StoreOption) core.Store {
	t.Helper()
	all := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, driverfx.LocalFS(t), all...)
	return s
}

// InitWithRoot is Init plus the driver root for on-disk inspection.
func InitWithRoot(t testing.TB, opts ...core.StoreOption) (core.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	all := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, drv, all...)
	return s, drv.Root()
}

// InitOn wires Init around a caller-provided driver. Caller also
// owns the index — pass core.WithStoreIndex explicitly.
func InitOn(t testing.TB, drv driver.Driver, opts ...core.StoreOption) core.Store {
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
func InitEncryptedOn(t testing.TB, drv driver.Driver, pass string, extra ...core.StoreOption) {
	t.Helper()
	opts := append([]core.StoreOption{
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithPassphrase(StaticPP(pass)),
	}, extra...)
	if _, _, err := core.InitStore(context.Background(), drv, append([]core.StoreOption{core.WithHashRegistry(Hashes())}, opts...)...); err != nil {
		t.Fatalf("storefx.InitEncryptedOn: %v", err)
	}
}

// InitPlainOn is the Plain-DEK counterpart of InitEncryptedOn:
// bootstrap on a caller-supplied driver with a discarded index, no
// passphrase. Use to seed a Location for subsequent OpenStore
// scenarios that do not need a Reopener.
func InitPlainOn(t testing.TB, drv driver.Driver, extra ...core.StoreOption) {
	t.Helper()
	opts := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, extra...)
	if _, _, err := core.InitStore(context.Background(), drv, append([]core.StoreOption{core.WithHashRegistry(Hashes())}, opts...)...); err != nil {
		t.Fatalf("storefx.InitPlainOn: %v", err)
	}
}

// TryOpenOn calls core.OpenStore on a caller-supplied driver with
// a fresh in-memory index and the standard test hash registry.
// Returns the (Store, error) pair so the caller can assert on the
// failure mode itself — fatal-on-failure callers should use OpenOn.
func TryOpenOn(t testing.TB, drv driver.Driver, extra ...core.StoreOption) (core.Store, error) {
	t.Helper()
	opts := append([]core.StoreOption{
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(Hashes()),
	}, extra...)
	return core.OpenStore(context.Background(), drv, opts...)
}

// OpenOn is the fatal variant of TryOpenOn: it calls t.Fatalf when
// OpenStore returns an error and returns just the Store on success.
func OpenOn(t testing.TB, drv driver.Driver, extra ...core.StoreOption) core.Store {
	t.Helper()
	s, err := TryOpenOn(t, drv, extra...)
	if err != nil {
		t.Fatalf("storefx.OpenOn: %v", err)
	}
	return s
}

func initStore(t testing.TB, drv driver.Driver, opts ...core.StoreOption) (core.Store, []byte) {
	t.Helper()
	all := append([]core.StoreOption{core.WithHashRegistry(Hashes())}, opts...)
	s, kit, err := core.InitStore(context.Background(), drv, all...)
	if err != nil {
		t.Fatalf("storefx.Init: %v", err)
	}
	return s, kit
}

// InitPlain bootstraps a fresh Plain Store and returns a Reopener
// bound to the same (drv, idx) so the test can reopen the Location
// later. Use whenever the test exercises a reopen-flow; for
// single-Init tests, plain Init/InitWithRoot remain shorter.
func InitPlain(t testing.TB, extra ...core.StoreOption) (core.Store, *Reopener) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	opts := append([]core.StoreOption{core.WithStoreIndex(idx)}, extra...)
	s, _ := initStore(t, drv, opts...)
	return s, &Reopener{drv: drv, idx: idx}
}

// InitEncrypted bootstraps a fresh encrypted Store with the given
// passphrase and returns a Reopener. The Store is fully Unlocked
// after Init (per the Plain-DEK→Encrypted transition described in
// core/lifecycle.go's InitStore).
//
// extra options are appended to the engine's defaults; pass
// core.WithKDFParams or core.WithConfig as needed.
func InitEncrypted(t testing.TB, pass string, extra ...core.StoreOption) (core.Store, *Reopener) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	opts := append([]core.StoreOption{
		core.WithStoreIndex(idx),
		core.WithPassphrase(StaticPP(pass)),
	}, extra...)
	s, _ := initStore(t, drv, opts...)
	return s, &Reopener{drv: drv, idx: idx}
}

// InitEncryptedLocked bootstraps an encrypted Store and reopens it
// WITHOUT AutoUnlock so the returned Store is in StateLocked. The
// PassphraseProvider configured on the second open uses the same
// passphrase — tests that need a different provider can replace it
// via Reopener.Open with WithPassphrase override.
func InitEncryptedLocked(t testing.TB, pass string, extra ...core.StoreOption) (core.Store, *Reopener) {
	t.Helper()
	_, r := InitEncrypted(t, pass, extra...)
	s := r.Open(t,
		core.WithPassphrase(StaticPP(pass)),
	)
	return s, r
}

// Payload wraps a string as a domain.Artifact body.
func Payload(content string) domain.Artifact {
	return domain.Artifact{Payload: strings.NewReader(content)}
}

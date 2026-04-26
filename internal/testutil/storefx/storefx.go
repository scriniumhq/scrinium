// Package storefx supplies Store fixtures for tests. It wires
// driverfx.LocalFS + indexfx.Memory + a sha256-only HashRegistry
// into a ready-to-use core.Store, which is what the vast majority
// of tests need.
//
// Tests that diverge from the defaults pass core.StoreOption values
// to override individual pieces (custom index, custom hash registry,
// etc.) — the variadic opts accept anything core.InitStore accepts.
package storefx

import (
	"context"
	"crypto/sha256"
	"hash"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
)

// Hashes returns a HashRegistry populated with sha256, the project
// default. core.NewHashRegistry returns an empty registry — engine
// policy is that the host application chooses which hashers to
// bundle. Tests own that policy and pick sha256.
func Hashes() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// Init creates a fresh Store backed by a per-test localfs driver
// and an in-memory sqlite index. Hash registry has sha256
// registered. Caller-supplied opts append to (and can override)
// the defaults.
//
// This is the right helper for any test that needs "a working
// Store" without caring about the exact wiring.
func Init(t *testing.T, opts ...core.StoreOption) core.Store {
	t.Helper()
	all := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, driverfx.LocalFS(t), all...)
	return s
}

// InitWithRoot is Init plus the driver's root directory. Tests
// that inspect on-disk state (e.g., assert that store.json
// appeared) reach for this.
func InitWithRoot(t *testing.T, opts ...core.StoreOption) (core.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	all := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, drv, all...)
	return s, drv.Root()
}

// InitOn wires Init around a caller-provided driver. The caller
// also owns the StoreIndex choice — pass core.WithStoreIndex
// explicitly. Used by tests that need a non-default driver (a
// faulty wrapper, a disk-backed index, etc.) and don't want the
// defaults' in-memory index spun up only to be overridden.
func InitOn(t *testing.T, drv driver.Driver, opts ...core.StoreOption) core.Store {
	t.Helper()
	s, _ := initStore(t, drv, opts...)
	return s
}

// initStore is the shared body. Adds the sha256 hash registry (a
// near-universal default — every test in M1 needs it) and lets
// callers supply or override the StoreIndex. The Recovery Kit
// return is captured for the M2 tests that will inspect it; M1
// callers ignore it.
func initStore(t *testing.T, drv driver.Driver, opts ...core.StoreOption) (core.Store, []byte) {
	t.Helper()
	all := append([]core.StoreOption{core.WithHashRegistry(Hashes())}, opts...)
	s, kit, err := core.InitStore(context.Background(), drv, all...)
	if err != nil {
		t.Fatalf("storefx.Init: %v", err)
	}
	return s, kit
}

// Payload constructs a domain.Artifact whose body is the given
// string and whose Metadata is empty. The most common shape in
// tests that exercise Put/Get round-trips and don't care about
// metadata semantics.
func Payload(content string) domain.Artifact {
	return domain.Artifact{
		Payload: strings.NewReader(content),
	}
}

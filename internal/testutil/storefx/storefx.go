// Package storefx supplies Store fixtures for tests.
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

// Hashes returns a HashRegistry with sha256 registered.
func Hashes() domain.HashRegistry {
	return core.NewHashRegistry().
		Register("sha256", func() hash.Hash { return sha256.New() })
}

// Init: fresh Store on localfs + in-memory sqlite index + sha256.
// Caller opts append to (and can override) defaults.
func Init(t *testing.T, opts ...core.StoreOption) core.Store {
	t.Helper()
	all := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, driverfx.LocalFS(t), all...)
	return s
}

// InitWithRoot is Init plus the driver root for on-disk inspection.
func InitWithRoot(t *testing.T, opts ...core.StoreOption) (core.Store, string) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	all := append([]core.StoreOption{core.WithStoreIndex(indexfx.Memory(t))}, opts...)
	s, _ := initStore(t, drv, all...)
	return s, drv.Root()
}

// InitOn wires Init around a caller-provided driver. Caller also
// owns the index — pass core.WithStoreIndex explicitly.
func InitOn(t *testing.T, drv driver.Driver, opts ...core.StoreOption) core.Store {
	t.Helper()
	s, _ := initStore(t, drv, opts...)
	return s
}

func initStore(t *testing.T, drv driver.Driver, opts ...core.StoreOption) (core.Store, []byte) {
	t.Helper()
	all := append([]core.StoreOption{core.WithHashRegistry(Hashes())}, opts...)
	s, kit, err := core.InitStore(context.Background(), drv, all...)
	if err != nil {
		t.Fatalf("storefx.Init: %v", err)
	}
	return s, kit
}

// Payload wraps a string as a domain.Artifact body.
func Payload(content string) domain.Artifact {
	return domain.Artifact{Payload: strings.NewReader(content)}
}

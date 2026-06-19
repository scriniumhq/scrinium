package store_test

import (
	"bytes"
	"context"
	"testing"

	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// KeyResolver promotion: an encrypted Store has no resolver until it is
// unlocked (or auto-unlocked, or initialised), at which point the engine
// installs the default static resolver derived from the DEK — UNLESS the
// host supplied its own, which must survive untouched. The internal
// resolver field is observed through the StoreKeyResolver bridge in
// export_test.go; everything else here is the public Init/Open surface.

func TestKeyResolverPromotion_OnUnlock(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Open without AutoUnlock — Locked, no DEK yet.
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) != nil {
		t.Error("Locked Store should have no KeyResolver yet")
	}
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("Unlock should populate the default KeyResolver")
	}
}

func TestKeyResolverPromotion_OnAutoUnlock(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("AutoUnlock should populate default KeyResolver")
	}
}

func TestKeyResolverPromotion_RespectsCustomResolver(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	customDEK := bytes.Repeat([]byte{0xAB}, 32)
	custom := pipeline.NewStaticKeyResolver(customDEK)

	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithKeyResolver(custom),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := store.StoreKeyResolver(s)
	if got == nil {
		t.Fatal("KeyResolver should not be nil")
	}
	// The custom resolver MUST have survived AutoUnlock — verify by
	// querying it (it returns customDEK, not the engine's DEK).
	keys, err := got.GetKeys("")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], customDEK) {
		t.Error("AutoUnlock overwrote the caller's custom KeyResolver")
	}
}

func TestKeyResolverPromotion_OnInitStore(t *testing.T) {
	drv := driverfx.LocalFS(t)
	s, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("InitStore on encrypted Store should populate default KeyResolver")
	}
}

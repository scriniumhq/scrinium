package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// E2E for the ADR-110 connection check at the real OpenStore boundary:
// init a store with governance defaults, close it, reconnect with
// client configs of every class and assert the class-mapped outcome.

func TestOpenStore_ConnectionClasses(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	hashes := storefx.Hashes()

	retention := 90 * 24 * time.Hour
	st, _, err := store.InitStore(ctx, drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(hashes),
		store.WithConfig(config.StoreConfig{
			DeletionPolicy:  config.DeletionPolicyRetention,
			RetentionPeriod: retention,
		}),
		store.WithLivenessInterval(-1),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopen := func(cfg config.StoreConfig) error {
		s, err := store.OpenStore(ctx, drv,
			store.WithStoreIndex(idx),
			store.WithHashRegistry(hashes),
			store.WithConfig(cfg),
			store.WithLivenessInterval(-1),
		)
		if err == nil {
			_ = s.Close()
		}
		return err
	}

	// Matching class II — passes.
	if err := reopen(config.StoreConfig{
		DeletionPolicy:  config.DeletionPolicyRetention,
		RetentionPeriod: retention,
	}); err != nil {
		t.Fatalf("matching governance config must open, got %v", err)
	}

	// Diverging class II — governance refusal: retention cannot be
	// escaped by connecting with a softer config.
	err = reopen(config.StoreConfig{RetentionPeriod: 24 * time.Hour})
	if !errors.Is(err, errs.ErrGovernanceMismatch) {
		t.Errorf("class II divergence: want ErrGovernanceMismatch, got %v", err)
	}
	err = reopen(config.StoreConfig{DeletionPolicy: config.DeletionPolicyFree})
	if !errors.Is(err, errs.ErrGovernanceMismatch) {
		t.Errorf("DeletionPolicy escape: want ErrGovernanceMismatch, got %v", err)
	}

	// Diverging class III — the session overlay: the connection opens
	// and lives by its own values (asserted end-to-end in
	// TestOpenStore_SessionOverlay below).
	if err := reopen(config.StoreConfig{BlobStorage: config.BlobStorageInline, InlineBlobLimit: 4096}); err != nil {
		t.Errorf("class III divergence must open with an overlay, got %v", err)
	}

	// Diverging class I — the classic immutable refusal, untouched.
	err = reopen(config.StoreConfig{PathTopology: config.PathTopologyFlat})
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Errorf("class I divergence: want ErrConfigMismatch, got %v", err)
	}

	// And the store is still openable afterwards — refusals leave no
	// residue.
	if err := reopen(config.StoreConfig{}); err != nil {
		t.Fatalf("empty client config must open, got %v", err)
	}
}

// UpdateConfig remains the admin path: the very change a connection is
// refused for goes through cleanly as an explicit act, versioned.
func TestUpdateConfig_IsTheAdminPathForGovernance(t *testing.T) {
	ctx := context.Background()
	st, _, _ := storefx.InitShared(t, store.WithLivenessInterval(-1))

	before := st.Config()
	req := before
	req.RetentionPeriod = before.RetentionPeriod + 48*time.Hour
	if err := st.UpdateConfig(ctx, req); err != nil {
		t.Fatalf("UpdateConfig (governance change): %v", err)
	}
	if got := st.Config().RetentionPeriod; got != req.RetentionPeriod {
		t.Errorf("RetentionPeriod = %v, want %v", got, req.RetentionPeriod)
	}
}

// TestOpenStore_SessionOverlay: the connection's class-III preferences
// take effect on its own writes, while the store's persisted defaults
// stay untouched — Config() shows the defaults, ConfigHistory grows no
// version (the overlay is memory-only, INV-110-4).
func TestOpenStore_SessionOverlay(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	hashes := storefx.Hashes()

	st, _, err := store.InitStore(ctx, drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(hashes),
		store.WithLivenessInterval(-1),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if st.Config().BlobStorage != config.BlobStorageTarget {
		t.Fatalf("default BlobStorage must be Target, got %q", st.Config().BlobStorage)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reconnect with an Inline session.
	st, err = store.OpenStore(ctx, drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(hashes),
		store.WithConfig(config.StoreConfig{
			BlobStorage:     config.BlobStorageInline,
			InlineBlobLimit: 1 << 16,
		}),
		store.WithLivenessInterval(-1),
	)
	if err != nil {
		t.Fatalf("OpenStore with overlay: %v", err)
	}
	defer func() { _ = st.Close() }()

	// The session writes Inline...
	id, err := st.Put(ctx, artifactfx.Payload("small payload under the session inline limit"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rh, err := st.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	layout := rh.Manifest().LayoutHeader.BlobStorage
	_ = rh.Close()
	if layout != domain.LayoutInline {
		t.Errorf("session Put must honour the overlay: LayoutHeader.BlobStorage = %q, want %q",
			layout, domain.LayoutInline)
	}

	// ...while the admin view still shows the store defaults, and no
	// config version was born.
	if got := st.Config().BlobStorage; got != config.BlobStorageTarget {
		t.Errorf("Config() must show the store defaults, got %q", got)
	}
	hist, err := st.ConfigHistory(ctx)
	if err != nil {
		t.Fatalf("ConfigHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Errorf("overlay must not persist: ConfigHistory has %d versions, want 1", len(hist))
	}
}

// TestOpenStore_SessionOverridesDeny: the admin flips the class-II
// knob — a diverging class-III connection is refused like governance.
func TestOpenStore_SessionOverridesDeny(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	hashes := storefx.Hashes()

	st, _, err := store.InitStore(ctx, drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(hashes),
		store.WithConfig(config.StoreConfig{SessionOverrides: config.SessionOverridesDeny}),
		store.WithLivenessInterval(-1),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = store.OpenStore(ctx, drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(hashes),
		store.WithConfig(config.StoreConfig{BlobStorage: config.BlobStorageInline}),
		store.WithLivenessInterval(-1),
	)
	if !errors.Is(err, errs.ErrGovernanceMismatch) {
		t.Errorf("Deny must refuse a diverging class-III connection, got %v", err)
	}
}

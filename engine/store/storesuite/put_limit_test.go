package storesuite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// E2E for MaxArtifactSize (class II governance, ADR-110): a streaming
// guard on the write paths — Artifact declares no size, so the limit
// is enforced on the byte flow itself.

func TestMaxArtifactSize_EnforcedOnPut(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)

	st, _, err := store.InitStore(ctx, drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithConfig(config.StoreConfig{MaxArtifactSize: 1024}),
		store.WithLivenessInterval(-1),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Exactly at the limit — passes (the boundary is inclusive).
	if _, err := st.Put(ctx, artifactfx.PayloadSized(1024)); err != nil {
		t.Fatalf("Put at the limit must pass, got %v", err)
	}
	// One byte over — refused with the sentinel.
	if _, err := st.Put(ctx, artifactfx.PayloadSized(1025)); !errors.Is(err, errs.ErrArtifactTooLarge) {
		t.Fatalf("Put over the limit: want ErrArtifactTooLarge, got %v", err)
	}
	// The headless write path is capped by the same governance limit.
	// WriteHeadless is not on the public Store interface — it is the
	// engine-internal HeadlessDataPlane seam, obtained via HeadlessOf
	// (the same type-assertion pattern the checkpoint agent uses).
	hw, ok := store.HeadlessOf(st)
	if !ok {
		t.Fatal("engine store must expose the headless data plane")
	}
	if _, err := hw.WriteHeadless(ctx, strings.NewReader(string(artifactfx.SizedBytes(2048)))); !errors.Is(err, errs.ErrArtifactTooLarge) {
		t.Fatalf("WriteHeadless over the limit: want ErrArtifactTooLarge, got %v", err)
	}
	// The store stays healthy after refusals.
	if _, err := st.Put(ctx, artifactfx.Payload("small after refusal")); err != nil {
		t.Fatalf("Put after refusals must pass, got %v", err)
	}
}

// Zero means unlimited — the default store takes a payload larger than
// any would-be small default.
func TestMaxArtifactSize_ZeroIsUnlimited(t *testing.T) {
	ctx := context.Background()
	st, _, _ := storefx.InitShared(t, store.WithLivenessInterval(-1))

	if got := st.Config().MaxArtifactSize; got != 0 {
		t.Fatalf("default MaxArtifactSize = %d, want 0 (unlimited)", got)
	}
	if _, err := st.Put(ctx, artifactfx.PayloadSized(1<<20)); err != nil {
		t.Fatalf("unlimited store must take 1 MiB, got %v", err)
	}
}

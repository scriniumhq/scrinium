package rebuild_test

import (
	"context"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent/rebuild"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// TestRebuild_FullScan_DecodesEncryptedManifests drives the KeyProvider
// path: an encrypted Store writes a manifest to the Location, then a
// rebuild into a fresh index reconstructs it. The agent obtains the key
// material from the Store itself (store.ManifestKeyProvider), so encrypted
// manifests are decoded and reindexed rather than skipped.
func TestRebuild_FullScan_DecodesEncryptedManifests(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	rec := eventfx.New()

	st, _, err := store.InitStore(ctx, drv,
		store.WithHashRegistry(storefx.Hashes()),
		store.WithStoreIndex(idx),
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithPublisher(rec),
	)
	if err != nil {
		t.Fatalf("InitStore (encrypted): %v", err)
	}
	if _, err := st.Put(ctx, artifactfx.Payload("payload that lands as an encrypted manifest file"),
		domain.WithNamespace("r")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Rebuild into a fresh, empty index — the agent must reconstruct it
	// from the on-disk (encrypted) manifests.
	rebuilt := indexfx.Memory(t)
	a, err := rebuild.NewRebuildIndexAgent(st, drv, rebuilt, rec, rebuildHostID, "store-rebuild",
		rebuild.RebuildConfig{})
	if err != nil {
		t.Fatalf("NewRebuildIndexAgent: %v", err)
	}

	if _, err := a.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.Stats().ManifestsIndexed; got < 1 {
		t.Errorf("ManifestsIndexed = %d, want >= 1 (encrypted manifest decoded via KeyProvider)", got)
	}
}

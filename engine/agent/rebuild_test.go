package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

const rebuildHostID = "rebuild-host-0001"

// rebuildFixture writes artifacts through a store (so manifest files
// land on the Location and the original index is populated), then
// exposes a SECOND, empty index on the same driver. That empty index is
// the rebuild target: it stands in for a lost index the agent must
// reconstruct from the on-disk manifests.
type rebuildFixture struct {
	store   store.Store
	drv     driver.Driver
	origIdx index.StoreIndex
	rec     *eventfx.Recorder
	rebuilt index.StoreIndex
}

func newRebuildFixture(t *testing.T, opts ...store.StoreOption) rebuildFixture {
	t.Helper()
	rec := eventfx.New()
	all := append([]store.StoreOption{store.WithPublisher(rec)}, opts...)
	st, drv, idx := storefx.InitShared(t, all...)
	return rebuildFixture{
		store:   st,
		drv:     drv,
		origIdx: idx,
		rec:     rec,
		rebuilt: indexfx.Memory(t),
	}
}

func (f rebuildFixture) put(t *testing.T, ns, data string) domain.ArtifactID {
	t.Helper()
	id, err := f.store.Put(context.Background(), artifactfx.Payload(data), store.WithNamespace(ns))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return id
}

func newRebuild(t *testing.T, f rebuildFixture, cfg agent.RebuildConfig) agent.RebuildIndexAgent {
	t.Helper()
	a, err := agent.NewRebuildIndexAgent(f.store, f.drv, f.rebuilt, f.rec, rebuildHostID, "store-rebuild", cfg)
	if err != nil {
		t.Fatalf("NewRebuildIndexAgent: %v", err)
	}
	return a
}

func TestNewRebuild_RequiresDeps(t *testing.T) {
	f := newRebuildFixture(t)
	cases := map[string]func() (agent.RebuildIndexAgent, error){
		"nil store": func() (agent.RebuildIndexAgent, error) {
			return agent.NewRebuildIndexAgent(nil, f.drv, f.rebuilt, f.rec, rebuildHostID, "", agent.RebuildConfig{})
		},
		"nil driver": func() (agent.RebuildIndexAgent, error) {
			return agent.NewRebuildIndexAgent(f.store, nil, f.rebuilt, f.rec, rebuildHostID, "", agent.RebuildConfig{})
		},
		"nil index": func() (agent.RebuildIndexAgent, error) {
			return agent.NewRebuildIndexAgent(f.store, f.drv, nil, f.rec, rebuildHostID, "", agent.RebuildConfig{})
		},
		"nil bus": func() (agent.RebuildIndexAgent, error) {
			return agent.NewRebuildIndexAgent(f.store, f.drv, f.rebuilt, nil, rebuildHostID, "", agent.RebuildConfig{})
		},
		"empty host": func() (agent.RebuildIndexAgent, error) {
			return agent.NewRebuildIndexAgent(f.store, f.drv, f.rebuilt, f.rec, "", "", agent.RebuildConfig{})
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := mk(); err == nil {
				t.Fatal("expected constructor error, got nil")
			}
		})
	}
}

func TestRebuild_FullScan_RecoversTargetManifests(t *testing.T) {
	f := newRebuildFixture(t)
	id1 := f.put(t, "r", "first artifact payload that overflows inline")
	id2 := f.put(t, "r", "second artifact payload also target sized")

	for _, id := range []domain.ArtifactID{id1, id2} {
		if ok, _ := f.rebuilt.ManifestExists(context.Background(), id); ok {
			t.Fatalf("rebuild target unexpectedly already has %s", id)
		}
	}

	a := newRebuild(t, f, agent.RebuildConfig{Source: agent.RebuildSourceFullScan})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil {
		t.Fatal("nil AgentResult")
	}

	for _, id := range []domain.ArtifactID{id1, id2} {
		ok, err := f.rebuilt.ManifestExists(context.Background(), id)
		if err != nil {
			t.Fatalf("ManifestExists(%s): %v", id, err)
		}
		if !ok {
			t.Errorf("manifest %s not recovered into the rebuilt index", id)
		}
	}
	st := a.Stats()
	if st.ManifestsIndexed < 2 {
		t.Errorf("ManifestsIndexed = %d, want >= 2", st.ManifestsIndexed)
	}
	if st.Source != agent.RebuildSourceFullScan {
		t.Errorf("Source = %q, want FullScan", st.Source)
	}
}

func TestRebuild_FullScan_RecoversInlineManifests(t *testing.T) {
	f := newRebuildFixture(t, store.WithConfig(domain.StoreConfig{
		BlobStorage:     domain.BlobStorageInline,
		InlineBlobLimit: 1024,
	}))
	id := f.put(t, "r", "x")

	a := newRebuild(t, f, agent.RebuildConfig{})
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ok, err := f.rebuilt.ManifestExists(context.Background(), id)
	if err != nil {
		t.Fatalf("ManifestExists: %v", err)
	}
	if !ok {
		t.Errorf("inline manifest %s not recovered", id)
	}
}

func TestRebuild_Validate_SnapshotSourceUnavailable(t *testing.T) {
	f := newRebuildFixture(t)
	a := newRebuild(t, f, agent.RebuildConfig{Source: agent.RebuildSourceSnapshot})
	if err := a.Validate(context.Background()); !errors.Is(err, errs.ErrNoSnapshot) {
		t.Fatalf("Validate(Snapshot) = %v, want ErrNoSnapshot", err)
	}
}

func TestRebuild_Validate_AutoSourcePasses(t *testing.T) {
	f := newRebuildFixture(t)
	a := newRebuild(t, f, agent.RebuildConfig{Source: agent.RebuildSourceAuto})
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("Validate(Auto) = %v, want nil", err)
	}
}

func TestRebuild_RecoveryKitUnsupported(t *testing.T) {
	f := newRebuildFixture(t)
	a := newRebuild(t, f, agent.RebuildConfig{RecoveryKit: []byte("kit")})
	if _, err := a.Run(context.Background()); !errors.Is(err, errs.ErrNotImplemented) {
		t.Fatalf("Run(RecoveryKit) = %v, want ErrNotImplemented", err)
	}
}

func TestRebuild_BlockedByForeignLease(t *testing.T) {
	f := newRebuildFixture(t)
	f.put(t, "r", "data large enough to be a target blob payload")
	now := time.Now()
	rec := leaseRecordJSON("other-host", now, now.Add(time.Hour), "RebuildIndex")
	if err := f.drv.Put(context.Background(),
		"system.state/maintenance/lease", strings.NewReader(rec)); err != nil {
		t.Fatalf("stage lease: %v", err)
	}
	a := newRebuild(t, f, agent.RebuildConfig{})
	if _, err := a.Run(context.Background()); err == nil {
		t.Fatal("Run with a live foreign maintenance lease = nil, want lease-held failure")
	}
}

func TestRebuild_CancelledContext(t *testing.T) {
	f := newRebuildFixture(t)
	f.put(t, "r", "data large enough to be a target blob payload")
	a := newRebuild(t, f, agent.RebuildConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx); err == nil {
		t.Fatal("Run(cancelled) = nil, want error")
	}
}

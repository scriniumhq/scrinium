package storesuite

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/config"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// Governance freshness on the liveness tick (ADR-110, INV-110-7). An
// "external admin" is, on disk, just a newer config.StoreConfig version
// published by another host. Here that other host is a SECOND live
// instance opened on the same driver, changing config through the
// public UpdateConfig — the real path an operator on another machine
// takes. The instance under test must pick the change up within a tick:
// agents and the Delete path read the active config, so one swap makes
// every governance consumer fresh at once.

// externalUpdate simulates another host: it opens a second store on the
// same driver, applies mutate() via the public UpdateConfig, and closes.
// The instance under test learns of it only through the on-disk config
// version bump — no shared memory, exactly like a real second host.
func externalUpdate(t *testing.T, drv *localfs.Driver, idx index.StoreIndex, mutate func(*config.StoreConfig)) {
	t.Helper()
	// Another host = a second live store on the same driver. It shares
	// the test's index so the artifact the instance-under-test wrote
	// stays visible (the config we mutate lives on the shared driver
	// regardless); the point is the config version bump, not index
	// isolation.
	other := storefx.OpenOn(t, drv, store.WithStoreIndex(idx))
	cfg := other.Config()
	mutate(&cfg)
	if err := other.UpdateConfig(context.Background(), cfg); err != nil {
		_ = other.Close()
		t.Fatalf("external host UpdateConfig: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("external host Close: %v", err)
	}
}

func TestConfigFreshness_ExternalChangePickedUp(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t,
		store.WithPublisher(rec),
		store.WithLivenessInterval(probeTick),
	)
	t.Cleanup(func() { _ = st.Close() })

	before := st.Config()
	newRetention := before.RetentionPeriod + 72*time.Hour

	externalUpdate(t, drv, idx, func(c *config.StoreConfig) {
		c.RetentionPeriod = newRetention
	})

	eventually(t, "instance to pick up the external governance change", func() bool {
		return st.Config().RetentionPeriod == newRetention
	})
	eventually(t, "EventConfigUpdated to be published for the refresh", func() bool {
		return rec.Count(event.EventConfigUpdated) > 0
	})
}

// The instance's own UpdateConfig bumps the config version itself — the
// freshness sentinel must not re-announce it as an external change.
// Exactly one EventConfigUpdated.
func TestConfigFreshness_LocalUpdateNoSelfEcho(t *testing.T) {
	rec := eventfx.New()
	st, _, _ := storefx.InitShared(t,
		store.WithPublisher(rec),
		store.WithLivenessInterval(probeTick),
	)
	t.Cleanup(func() { _ = st.Close() })

	req := st.Config()
	req.RetentionPeriod += 24 * time.Hour
	if err := st.UpdateConfig(context.Background(), req); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	time.Sleep(10 * probeTick)
	if n := rec.Count(event.EventConfigUpdated); n != 1 {
		t.Errorf("EventConfigUpdated published %d times, want exactly 1 (no sentinel self-echo)", n)
	}
	if got := st.Config().RetentionPeriod; got != req.RetentionPeriod {
		t.Errorf("RetentionPeriod = %v, want %v", got, req.RetentionPeriod)
	}
}

// The freshness swap is not cosmetic: a NoDelete flipped by an external
// admin starts refusing this instance's Delete within a tick.
func TestConfigFreshness_GovernanceReachesDeletePath(t *testing.T) {
	st, drv, idx := storefx.InitShared(t, store.WithLivenessInterval(probeTick))
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	id, err := st.Put(ctx, artifactfx.Payload("governed payload"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	externalUpdate(t, drv, idx, func(c *config.StoreConfig) {
		c.DeletionPolicy = config.DeletionPolicyNoDelete
	})
	eventually(t, "NoDelete to reach this instance", func() bool {
		return st.Config().DeletionPolicy == config.DeletionPolicyNoDelete
	})

	if err := st.Delete(ctx, id); !errors.Is(err, errs.ErrDeletionForbidden) {
		t.Errorf("Delete under externally-set NoDelete: want ErrDeletionForbidden, got %v", err)
	}
}

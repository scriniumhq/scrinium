package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// E2E for governance freshness on the liveness tick (ADR-110,
// INV-110-7): an "external admin" — another host's UpdateConfig — is,
// on disk, just a new store.config version. This instance must pick it
// up within a tick: agents and the Delete path read snapshotConfig, so
// the swap makes every governance consumer fresh at once.

func TestConfigFreshness_ExternalChangePickedUp(t *testing.T) {
	rec := eventfx.New()
	st, drv, _ := storefx.InitShared(t,
		store.WithPublisher(rec),
		store.WithLivenessInterval(probeTick),
	)
	t.Cleanup(func() { _ = st.Close() })

	before := st.Config()
	newRetention := before.RetentionPeriod + 72*time.Hour

	// Another host publishes a new version: on disk that is the whole
	// act — no message, no shared memory.
	external := before
	external.RetentionPeriod = newRetention
	if _, err := store.WriteConfig(context.Background(), drv, storefx.Hashes(), external); err != nil {
		t.Fatalf("external Write: %v", err)
	}

	eventually(t, "instance to pick up the external governance change", func() bool {
		return st.Config().RetentionPeriod == newRetention
	})
	eventually(t, "EventConfigUpdated to be published for the refresh", func() bool {
		return rec.Count(event.EventConfigUpdated) > 0
	})
}

// TestConfigFreshness_LocalUpdateNoSelfEcho: the instance's own
// UpdateConfig bumps lastConfigSeq itself — the sentinel must not
// re-announce it as an external change (exactly one EventConfigUpdated).
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

// TestConfigFreshness_GovernanceReachesDeletePath: the freshness swap
// is not cosmetic — a NoDelete flipped by an "external admin" starts
// refusing this instance's Delete within a tick.
func TestConfigFreshness_GovernanceReachesDeletePath(t *testing.T) {
	st, drv, _ := storefx.InitShared(t, store.WithLivenessInterval(probeTick))
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	id, err := st.Put(ctx, artifactfx.Payload("governed payload"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	external := st.Config()
	external.DeletionPolicy = domain.DeletionPolicyNoDelete
	if _, err := store.WriteConfig(ctx, drv, storefx.Hashes(), external); err != nil {
		t.Fatalf("external Write: %v", err)
	}
	eventually(t, "NoDelete to reach this instance", func() bool {
		return st.Config().DeletionPolicy == domain.DeletionPolicyNoDelete
	})

	if err := st.Delete(ctx, id); !errors.Is(err, errs.ErrDeletionForbidden) {
		t.Errorf("Delete under externally-set NoDelete: want ErrDeletionForbidden, got %v", err)
	}
}

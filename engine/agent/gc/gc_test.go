package gc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/gc"
	"scrinium.dev/engine/agent/internal/leasefx"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

const gcHostID = "gc-host-0001"

type gcFixture struct {
	store store.Store
	drv   *localfs.Driver
	idx   index.StoreIndex
	rec   *eventfx.Recorder
	root  string
}

// newGCFixture builds a store sharing one index with the agent. grace
// and policy go into StoreConfig, which RunOnce reads via store.Config().
func newGCFixture(t *testing.T, grace time.Duration, policy domain.GCLeasePolicy) gcFixture {
	t.Helper()
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec),
		store.WithConfig(domain.StoreConfig{
			TombstoneGracePeriod: grace,
			GCLeasePolicy:        policy,
		}))
	return gcFixture{store: st, drv: drv, idx: idx, rec: rec, root: drv.Root()}
}

func (f gcFixture) putAndOrphan(t *testing.T, data string) (domain.ArtifactID, string) {
	t.Helper()
	ctx := context.Background()
	id, err := f.store.Put(ctx, artifactfx.Payload(data), domain.WithNamespace("g"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rh, err := f.store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ref := string(rh.Manifest().BlobRef)
	_ = rh.Close()
	// Logical delete: manifest gone, ref_count -> 0, blob FILE retained
	// on disk (physical removal is the GC agent's job).
	if err := f.store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	return id, ref
}

func (f gcFixture) blobPath(t *testing.T, ref string) string {
	t.Helper()
	rel, err := artifact.BlobPath(domain.PathTopologySharded, domain.BlobTypeRegular, ref)
	if err != nil {
		t.Fatalf("BlobPath: %v", err)
	}
	return filepath.Join(f.root, rel)
}

func (f gcFixture) fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func newGC(t *testing.T, f gcFixture, cfg gc.GCConfig) gc.GCAgent {
	t.Helper()
	a, err := gc.NewGCAgent(f.store, f.drv, f.idx, f.rec, gcHostID, "store-gc", cfg)
	if err != nil {
		t.Fatalf("NewGCAgent: %v", err)
	}
	return a
}

func TestNewGC_RequiresDeps(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	cases := map[string]func() (gc.GCAgent, error){
		"nil store": func() (gc.GCAgent, error) {
			return gc.NewGCAgent(nil, f.drv, f.idx, f.rec, gcHostID, "", gc.GCConfig{})
		},
		"nil driver": func() (gc.GCAgent, error) {
			return gc.NewGCAgent(f.store, nil, f.idx, f.rec, gcHostID, "", gc.GCConfig{})
		},
		"nil index": func() (gc.GCAgent, error) {
			return gc.NewGCAgent(f.store, f.drv, nil, f.rec, gcHostID, "", gc.GCConfig{})
		},
		"nil bus": func() (gc.GCAgent, error) {
			return gc.NewGCAgent(f.store, f.drv, f.idx, nil, gcHostID, "", gc.GCConfig{})
		},
		"empty host": func() (gc.GCAgent, error) {
			return gc.NewGCAgent(f.store, f.drv, f.idx, f.rec, "", "", gc.GCConfig{})
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

func TestGC_MarkTombstonesOrphan(t *testing.T) {
	// Large grace: Mark must tombstone, Sweep must NOT remove yet.
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	_, ref := f.putAndOrphan(t, "orphan me")
	blob := f.blobPath(t, ref)
	if !f.fileExists(blob) {
		t.Fatalf("blob file missing before GC: %s", blob)
	}

	a := newGC(t, f, gc.GCConfig{})
	stats, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.MarkedBlobs < 1 {
		t.Errorf("MarkedBlobs = %d, want >= 1", stats.MarkedBlobs)
	}
	if stats.RemovedBlobs != 0 {
		t.Errorf("RemovedBlobs = %d, want 0 (grace not elapsed)", stats.RemovedBlobs)
	}
	// Original gone, tombstone present.
	if f.fileExists(blob) {
		t.Errorf("original blob still present after Mark: %s", blob)
	}
	marked, _, err := f.drv.TombstoneInfo(context.Background(), relPath(t, f, ref))
	if err != nil {
		t.Fatalf("TombstoneInfo: %v", err)
	}
	if !marked {
		t.Error("blob not tombstoned after Mark")
	}
}

func TestGC_SweepRemovesAfterGrace(t *testing.T) {
	// TombstoneGracePeriod has a 1h floor (MinTombstoneGracePeriod) and
	// a zero value is rejected, so "instant sweep" via grace=0 is not a
	// thing. Instead: Mark on cycle 1, age the marker past the grace
	// window with Chtimes, then cycle 2 must Sweep it. Deterministic,
	// no sleeping.
	grace := time.Hour
	f := newGCFixture(t, grace, domain.GCLeaseSingleHost)
	_, ref := f.putAndOrphan(t, "sweep me")
	blob := f.blobPath(t, ref)

	a := newGC(t, f, gc.GCConfig{})

	// Cycle 1 — Mark only (fresh tombstone is younger than grace).
	if _, err := a.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	tomb := blob + ".tombstone"
	if !f.fileExists(tomb) {
		t.Fatalf("tombstone missing after Mark: %s", tomb)
	}

	// Age the marker well past the grace window.
	old := time.Now().Add(-2 * grace)
	if err := os.Chtimes(tomb, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Cycle 2 — Sweep (grace now elapsed).
	stats, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if stats.RemovedBlobs < 1 {
		t.Errorf("RemovedBlobs = %d, want >= 1 after grace elapsed", stats.RemovedBlobs)
	}
	if f.fileExists(blob) || f.fileExists(tomb) {
		t.Errorf("blob/tombstone still on disk after Sweep")
	}
	if f.rec.Count("store.blob_physically_deleted") < 1 {
		t.Errorf("EventBlobPhysicallyDeleted not emitted")
	}
	var orphans int
	if err := f.idx.ListOrphanBlobs(context.Background(), func(string) error {
		orphans++
		return nil
	}); err != nil {
		t.Fatalf("ListOrphanBlobs: %v", err)
	}
	if orphans != 0 {
		t.Errorf("orphan rows after Sweep = %d, want 0", orphans)
	}
}

func TestGC_RevivedBlobSurvivesSweep(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	id, ref := f.putAndOrphan(t, "revive me")
	_ = id

	// Revive before GC: a new Put of the same bytes dedups onto the
	// same blob_ref (dedup key is content+size+identity, ADR-58, not
	// namespace) and bumps ref_count back above zero. The orphan row
	// still exists, but it is no longer ref_count=0 — so even with
	// grace=0 the Sweep's DeleteOrphanBlob guard must keep it.
	if _, err := f.store.Put(context.Background(), artifactfx.Payload("revive me"),
		domain.WithNamespace("g2")); err != nil {
		t.Fatalf("revive Put: %v", err)
	}

	a := newGC(t, f, gc.GCConfig{})
	stats, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.RemovedBlobs != 0 {
		t.Errorf("RemovedBlobs = %d, want 0 (blob was revived)", stats.RemovedBlobs)
	}
	// The blob is live again — still resolvable.
	if _, err := f.idx.Resolve(context.Background(), ref); err != nil {
		t.Errorf("revived blob no longer resolvable: %v", err)
	}
}

func TestGC_SingleHostTakesNoLease(t *testing.T) {
	// A live foreign gc/lease must NOT block a SingleHost cycle (it
	// never looks at the lease).
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	f.putAndOrphan(t, "data")
	leasefx.StageForeign(t, f.drv, "system.state/gc/lease", "other-host", "GC", time.Hour)
	a := newGC(t, f, gc.GCConfig{})
	if _, err := a.RunOnce(context.Background()); err != nil {
		t.Fatalf("SingleHost RunOnce must ignore the lease, got %v", err)
	}
}

func TestGC_LeaderElectionBlockedByForeignLease(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseLeaderElection)
	f.putAndOrphan(t, "data")
	leasefx.StageForeign(t, f.drv, "system.state/gc/lease", "other-host", "GC", time.Hour)
	a := newGC(t, f, gc.GCConfig{LeaseTTL: time.Minute})
	if _, err := a.RunOnce(context.Background()); err == nil {
		t.Fatal("LeaderElection RunOnce with a live foreign lease = nil, want lease-held failure")
	}
}

func TestGC_CancelledContext(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	f.putAndOrphan(t, "data")
	a := newGC(t, f, gc.GCConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce(cancelled) = nil, want error")
	}
}

func TestGC_RunStopsOnContextCancel(t *testing.T) {
	f := newGCFixture(t, time.Hour, domain.GCLeaseSingleHost)
	a := newGC(t, f, gc.GCConfig{ScanInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one-shot agent: a cancelled context must abort Run promptly
	_, err := a.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if st, _ := a.Status(); st != agent.StateFaulted {
		t.Errorf("state after cancel = %v, want StateFaulted", st)
	}
}

// relPath returns the driver-relative blob path (what TombstoneInfo
// expects), as opposed to blobPath which is absolute for os checks.
func relPath(t *testing.T, f gcFixture, ref string) string {
	t.Helper()
	rel, err := artifact.BlobPath(domain.PathTopologySharded, domain.BlobTypeRegular, ref)
	if err != nil {
		t.Fatalf("BlobPath: %v", err)
	}
	return rel
}

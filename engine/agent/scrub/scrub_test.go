package scrub_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/leasefx"
	"scrinium.dev/engine/agent/scrub"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

const scrubHostID = "scrub-host-0001"

// scrubFixture builds a real store sharing ONE index with the agent, on
// a localfs driver whose root we keep so tests can tamper blob files on
// disk. Store and agent must share the index, or a blob_ref written by
// the store would never appear in the agent's ListUnverifiedBlobs.
type scrubFixture struct {
	store store.Store
	drv   *localfs.Driver
	idx   index.StoreIndex
	rec   *eventfx.Recorder
	root  string
}

func newScrubFixture(t *testing.T) scrubFixture {
	t.Helper()
	return newScrubFixtureCfg(t)
}

// newScrubFixtureInline configures the store for Inline layout so a
// small payload assembles to an Inline manifest (no blobs row),
// exercising the scrub manifest pass.
func newScrubFixtureInline(t *testing.T) scrubFixture {
	t.Helper()
	return newScrubFixtureCfg(t, store.WithConfig(domain.StoreConfig{
		BlobStorage:     domain.BlobStorageInline,
		InlineBlobLimit: 1024,
	}))
}

func newScrubFixtureCfg(t *testing.T, opts ...store.StoreOption) scrubFixture {
	t.Helper()
	rec := eventfx.New()
	// InitShared gives the Store, its concrete driver (for Root and to
	// hand to the agent), and the shared index the agent must observe.
	all := append([]store.StoreOption{store.WithPublisher(rec)}, opts...)
	st, drv, idx := storefx.InitShared(t, all...)
	return scrubFixture{store: st, drv: drv, idx: idx, rec: rec, root: drv.Root()}
}

func (f scrubFixture) put(t *testing.T, ns, data string) domain.ArtifactID {
	t.Helper()
	id, err := f.store.Put(context.Background(),
		artifactfx.Payload(data),
		domain.WithNamespace(ns))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return id
}

func (f scrubFixture) blobRefOf(t *testing.T, id domain.ArtifactID) string {
	t.Helper()
	rh, err := f.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	return string(rh.Manifest().PrimaryBlobRef())
}

// tamperBlob flips a byte in the on-disk blob file for blobRef.
func (f scrubFixture) tamperBlob(t *testing.T, blobRef string) {
	t.Helper()
	rel, err := artifact.BlobPath(domain.PathTopologySharded, domain.BlobTypeRegular, blobRef)
	if err != nil {
		t.Fatalf("BlobPath: %v", err)
	}
	p := filepath.Join(f.root, rel)
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("blob unexpectedly empty")
	}
	content[0] ^= 0xff
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatalf("write tampered blob: %v", err)
	}
}

func newScrub(t *testing.T, f scrubFixture, cfg scrub.ScrubConfig) scrub.ScrubAgent {
	t.Helper()
	a, err := scrub.NewScrubAgent(f.store, f.drv, f.idx, f.rec, scrubHostID, "store-scrub", cfg)
	if err != nil {
		t.Fatalf("NewScrubAgent: %v", err)
	}
	return a
}

// forceCfg verifies everything regardless of last_verified_at, so a
// freshly-written artifact is eligible within the same test.
func forceCfg() scrub.ScrubConfig {
	return scrub.ScrubConfig{Force: true}
}

func TestNewScrub_RequiresDeps(t *testing.T) {
	f := newScrubFixture(t)
	cases := map[string]func() (scrub.ScrubAgent, error){
		"nil store": func() (scrub.ScrubAgent, error) {
			return scrub.NewScrubAgent(nil, f.drv, f.idx, f.rec, scrubHostID, "", scrub.ScrubConfig{})
		},
		"nil driver": func() (scrub.ScrubAgent, error) {
			return scrub.NewScrubAgent(f.store, nil, f.idx, f.rec, scrubHostID, "", scrub.ScrubConfig{})
		},
		"nil index": func() (scrub.ScrubAgent, error) {
			return scrub.NewScrubAgent(f.store, f.drv, nil, f.rec, scrubHostID, "", scrub.ScrubConfig{})
		},
		"nil bus": func() (scrub.ScrubAgent, error) {
			return scrub.NewScrubAgent(f.store, f.drv, f.idx, nil, scrubHostID, "", scrub.ScrubConfig{})
		},
		"empty host": func() (scrub.ScrubAgent, error) {
			return scrub.NewScrubAgent(f.store, f.drv, f.idx, f.rec, "", "", scrub.ScrubConfig{})
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

func TestScrub_HappyPath_VerifiesAndStamps(t *testing.T) {
	f := newScrubFixture(t)
	id := f.put(t, "v", "scrub me clean")
	blobRef := f.blobRefOf(t, id)
	f.rec.Clear()

	a := newScrub(t, f, forceCfg())
	stats, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.ScannedBlobs < 1 || stats.VerifiedBlobs < 1 {
		t.Errorf("stats = %+v, want at least 1 scanned and 1 verified", stats)
	}
	if stats.FailedBlobs != 0 {
		t.Errorf("FailedBlobs = %d, want 0 on clean store", stats.FailedBlobs)
	}
	if f.rec.Count("store.scrub_failed") != 0 {
		t.Errorf("unexpected scrub-failed events on a clean store")
	}

	// The blob is now stamped: a non-force pass with a fresh cutoff
	// must not see it.
	var seen []string
	if err := f.idx.ListUnverifiedBlobs(context.Background(), time.Now().Add(-time.Hour), func(ref string) error {
		seen = append(seen, ref)
		return nil
	}); err != nil {
		t.Fatalf("ListUnverifiedBlobs: %v", err)
	}
	for _, r := range seen {
		if r == blobRef {
			t.Errorf("blob %s still unverified after a clean scrub", blobRef)
		}
	}
}

func TestScrub_TamperedBlob_EmitsFailedAndContinues(t *testing.T) {
	f := newScrubFixture(t)
	// Two artifacts: one tampered, one clean. The clean one must still
	// be verified — a bad blob does not abort the pass.
	bad := f.put(t, "v", "tamper me")
	good := f.put(t, "v", "leave me be")
	badRef := f.blobRefOf(t, bad)
	goodRef := f.blobRefOf(t, good)
	f.tamperBlob(t, badRef)
	f.rec.Clear()

	a := newScrub(t, f, forceCfg())
	stats, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if stats.FailedBlobs < 1 {
		t.Errorf("FailedBlobs = %d, want >= 1 (tampered blob)", stats.FailedBlobs)
	}
	if n := f.rec.Count("store.scrub_failed"); n < 1 {
		t.Errorf("EventScrubFailed count = %d, want >= 1", n)
	}

	// The good blob was verified despite the bad one failing earlier
	// in the same pass: it must be stamped (gone from a fresh-cutoff
	// unverified list), the bad one still pending.
	pending := map[string]bool{}
	if err := f.idx.ListUnverifiedBlobs(context.Background(), time.Now().Add(-time.Hour), func(ref string) error {
		pending[ref] = true
		return nil
	}); err != nil {
		t.Fatalf("ListUnverifiedBlobs: %v", err)
	}
	if pending[goodRef] {
		t.Errorf("good blob %s not stamped; pass aborted on the bad blob?", goodRef)
	}
	if !pending[badRef] {
		t.Errorf("tampered blob %s should remain unverified (never stamped)", badRef)
	}
}

func TestScrub_InlineArtifact_CoveredByManifestPass(t *testing.T) {
	f := newScrubFixtureInline(t)
	// A small payload assembles to an Inline manifest (no blobs row), so
	// only the manifest pass can reach it. After scrub it must be
	// stamped at the manifest level.
	id := f.put(t, "v", "x")
	f.rec.Clear()

	a := newScrub(t, f, forceCfg())
	if _, err := a.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var pending []string
	if err := f.idx.ListUnverifiedManifests(context.Background(), time.Now().Add(-time.Hour), func(m domain.Manifest) error {
		pending = append(pending, string(m.ArtifactID))
		return nil
	}); err != nil {
		t.Fatalf("ListUnverifiedManifests: %v", err)
	}
	for _, p := range pending {
		if p == string(id) {
			t.Errorf("artifact %s manifest not stamped after scrub", id)
		}
	}
}

func TestScrub_BlockedByForeignLease(t *testing.T) {
	f := newScrubFixture(t)
	f.put(t, "v", "data")
	// Stage a live foreign scrub lease.
	leasefx.StageForeign(t, f.drv, "system.state/scrub/lease", "other-host", "Scrub", time.Hour)

	a := newScrub(t, f, forceCfg())
	if _, err := a.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce with a live foreign lease = nil err, want lease-held failure")
	}
}

func TestScrub_CancelledContext(t *testing.T) {
	f := newScrubFixture(t)
	f.put(t, "v", "data")
	a := newScrub(t, f, forceCfg())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce(cancelled) = nil, want error")
	}
}

func TestScrub_RunStopsOnContextCancel(t *testing.T) {
	f := newScrubFixture(t)
	a := newScrub(t, f, scrub.ScrubConfig{ScanInterval: time.Hour, Force: true})
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

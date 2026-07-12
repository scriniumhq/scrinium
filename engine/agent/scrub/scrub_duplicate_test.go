package scrub_test

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent/scrub"
)

// Coverage for decision R6, scrub Phase C: the duplicate-handle
// invariant check. The agent probes its index port for the optional
// index.DuplicateHandleAuditor capability (by-assertion); a backend
// with the capability contributes ScrubStats.DuplicateHandles, a
// backend without it is silently skipped. The sqlite GROUP BY itself
// is covered in engine/index/sqlite (duplicate_handles_test.go); the
// fakes here make the probe deterministic and independent of the
// resolve side-effects a staged transit row would cause on a real
// store.

// emptyIndex satisfies the agent's narrow index port with no data —
// phases A and B see nothing to verify.
type emptyIndex struct{}

func (emptyIndex) ListUnverifiedBlobs(ctx context.Context, before time.Time, cb func(string) error) error {
	return nil
}
func (emptyIndex) ListUnverifiedManifests(ctx context.Context, before time.Time, cb func(domain.Manifest) error) error {
	return nil
}
func (emptyIndex) ManifestsByBlobRef(ctx context.Context, blobRef string, cb func(domain.Manifest) error) error {
	return nil
}
func (emptyIndex) MarkVerified(ctx context.Context, blobRef string, ts time.Time) error {
	return nil
}
func (emptyIndex) MarkManifestVerified(ctx context.Context, id domain.ArtifactID, ts time.Time) error {
	return nil
}

// auditingIndex additionally implements the DuplicateHandleAuditor
// capability and reports two staged duplicates.
type auditingIndex struct{ emptyIndex }

func (auditingIndex) ListDuplicateHandles(ctx context.Context) ([]domain.ArtifactID, error) {
	return []domain.ArtifactID{"art-x", "art-y"}, nil
}

func TestScrub_DuplicateHandles_ReportedWithAuditor(t *testing.T) {
	f := newScrubFixture(t)

	a, err := scrub.NewScrubAgent(f.store, f.drv, auditingIndex{}, f.rec,
		scrubHostID, "store-scrub-dup", forceCfg())
	if err != nil {
		t.Fatalf("NewScrubAgent: %v", err)
	}
	stats, err := a.RunNow(context.Background())
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if stats.DuplicateHandles != 2 {
		t.Errorf("DuplicateHandles = %d, want 2", stats.DuplicateHandles)
	}
	// An invariant finding is a warning, not data corruption, and must
	// not fail the pass.
	if stats.FailedBlobs != 0 {
		t.Errorf("FailedBlobs = %d, want 0", stats.FailedBlobs)
	}
}

func TestScrub_DuplicateHandles_SkippedWithoutAuditor(t *testing.T) {
	f := newScrubFixture(t)

	a, err := scrub.NewScrubAgent(f.store, f.drv, emptyIndex{}, f.rec,
		scrubHostID, "store-scrub-dup-skip", forceCfg())
	if err != nil {
		t.Fatalf("NewScrubAgent: %v", err)
	}
	stats, err := a.RunNow(context.Background())
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if stats.DuplicateHandles != 0 {
		t.Errorf("DuplicateHandles = %d, want 0 (no auditor capability)", stats.DuplicateHandles)
	}
}

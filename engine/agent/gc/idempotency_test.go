package gc_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/gc"
)

// TestGC_RunIdempotentNoop verifies that a second Run over an
// already-collected store does no further work and stays clean — the
// single-modality contract is safe to call repeatedly (the scheduler
// does exactly that).
func TestGC_RunIdempotentNoop(t *testing.T) {
	grace := time.Hour
	f := newGCFixture(t, grace, domain.GCLeaseSingleHost)
	_, ref := f.putAndOrphan(t, "idempotent")
	a := newGC(t, f, gc.GCConfig{})

	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run #1 (mark): %v", err)
	}
	tomb := f.blobPath(t, ref) + ".tombstone"
	old := time.Now().Add(-2 * grace)
	if err := os.Chtimes(tomb, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run #2 (sweep): %v", err)
	}
	if res.Partial {
		t.Error("Run #2 reported Partial on a clean completion")
	}
	if res.Stats["removed_blobs"] < 1 {
		t.Fatalf("Run #2 removed_blobs = %d, want >= 1", res.Stats["removed_blobs"])
	}
	if st, _ := a.Status(); st != agent.StateIdle {
		t.Errorf("state after Run #2 = %v, want StateIdle", st)
	}

	res3, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run #3 (no-op): %v", err)
	}
	if res3.Stats["removed_blobs"] != 0 {
		t.Errorf("Run #3 removed_blobs = %d, want 0 (already collected)", res3.Stats["removed_blobs"])
	}
	if st, _ := a.Status(); st != agent.StateIdle {
		t.Errorf("state after Run #3 = %v, want StateIdle", st)
	}
}

func TestGC_RunResumesAfterCancel(t *testing.T) {
	grace := time.Hour
	f := newGCFixture(t, grace, domain.GCLeaseSingleHost)
	_, ref := f.putAndOrphan(t, "resume me")
	a := newGC(t, f, gc.GCConfig{})

	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("mark Run: %v", err)
	}
	tomb := f.blobPath(t, ref) + ".tombstone"
	old := time.Now().Add(-2 * grace)
	if err := os.Chtimes(tomb, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := a.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Run err = %v, want context.Canceled", err)
	}
	if res == nil || !res.Partial {
		t.Fatalf("cancelled Run: want non-nil Partial result, got %+v", res)
	}
	if res.Stats["removed_blobs"] != 0 {
		t.Errorf("cancelled Run removed_blobs = %d, want 0 (aborted before work)", res.Stats["removed_blobs"])
	}
	if st, _ := a.Status(); st != agent.StateFaulted {
		t.Errorf("state after cancel = %v, want StateFaulted", st)
	}

	res2, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	if res2.Stats["removed_blobs"] < 1 {
		t.Errorf("resume removed_blobs = %d, want >= 1 (the remainder)", res2.Stats["removed_blobs"])
	}
	if st, _ := a.Status(); st != agent.StateIdle {
		t.Errorf("state after resume = %v, want StateIdle", st)
	}
}

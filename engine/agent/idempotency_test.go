package agent_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
)

// TestGC_RunIdempotentNoop verifies that a second Run over an
// already-collected store does no further work and stays clean — the
// single-modality contract is safe to call repeatedly (the scheduler
// does exactly that).
func TestGC_RunIdempotentNoop(t *testing.T) {
	// GC deletion is two-phase: cycle 1 Marks (tombstone younger than
	// grace stays), cycle 2 Sweeps once the marker has aged past grace.
	// Aging is done with Chtimes (deterministic, no sleeping) — the same
	// technique the standalone removal test uses.
	grace := time.Hour
	f := newGCFixture(t, grace, domain.GCLeaseSingleHost)
	_, ref := f.putAndOrphan(t, "idempotent")
	a := newGC(t, f, agent.GCConfig{})

	// Cycle 1 — Mark only.
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run #1 (mark): %v", err)
	}
	tomb := f.blobPath(t, ref) + ".tombstone"
	old := time.Now().Add(-2 * grace)
	if err := os.Chtimes(tomb, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Cycle 2 — Sweep: removes the now-eligible orphan.
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

	// Cycle 3 — idempotent no-op: nothing left to collect.
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
	// Interrupt-then-resume: with a sweep-eligible orphan staged, a
	// cancelled Run must abort without sweeping, and a later Run with a
	// live context must process the remainder. Progress lives in the
	// Store (the aged tombstone), not the agent.
	grace := time.Hour
	f := newGCFixture(t, grace, domain.GCLeaseSingleHost)
	_, ref := f.putAndOrphan(t, "resume me")
	a := newGC(t, f, agent.GCConfig{})

	// Mark, then age the marker so it is sweep-eligible.
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("mark Run: %v", err)
	}
	tomb := f.blobPath(t, ref) + ".tombstone"
	old := time.Now().Add(-2 * grace)
	if err := os.Chtimes(tomb, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// A cancelled Run aborts before doing any sweep work.
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

	// Resume with a live context: the remainder is swept.
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

func TestScrub_RunRepeatable(t *testing.T) {
	f := newScrubFixture(t)
	a := newScrub(t, f, agent.ScrubConfig{Force: true})

	for i, label := range []string{"first", "second"} {
		res, err := a.Run(context.Background())
		if err != nil {
			t.Fatalf("%s Run: %v", label, err)
		}
		if res == nil || res.Partial {
			t.Errorf("%s Run: want non-nil non-Partial result, got %+v", label, res)
		}
		if st, _ := a.Status(); st != agent.StateIdle {
			t.Errorf("state after %s Run (i=%d) = %v, want StateIdle", label, i, st)
		}
	}
}

// TestCheckpoint_RunResumesAfterCancel verifies that a cancelled Checkpoint
// Run faults without producing a checkpoint, and a fresh Run then
// completes — the one-shot operation is re-runnable after interruption.
func TestCheckpoint_RunResumesAfterCancel(t *testing.T) {
	f := newCheckpointFixture(t)
	a := newCheckpoint(t, f, agent.CheckpointConfig{Interval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Run err = %v, want context.Canceled", err)
	}
	if st, _ := a.Status(); st != agent.StateFaulted {
		t.Errorf("state after cancel = %v, want StateFaulted", st)
	}

	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	if res == nil {
		t.Fatal("resume Run: nil AgentResult")
	}
	if st, _ := a.Status(); st != agent.StateIdle {
		t.Errorf("state after resume = %v, want StateIdle", st)
	}
}

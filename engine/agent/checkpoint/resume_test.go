package checkpoint_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/checkpoint"
)

// TestCheckpoint_RunResumesAfterCancel verifies that a cancelled Checkpoint
// Run faults without producing a checkpoint, and a fresh Run then
// completes — the one-shot operation is re-runnable after interruption.
func TestCheckpoint_RunResumesAfterCancel(t *testing.T) {
	f := newCheckpointFixture(t)
	a := newCheckpoint(t, f, checkpoint.CheckpointConfig{Interval: time.Hour})

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

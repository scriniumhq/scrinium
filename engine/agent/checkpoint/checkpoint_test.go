package checkpoint_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/checkpoint"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/leasefx"
	"scrinium.dev/testutil/storefx"
)

const checkpointHostID = "checkpoint-host-0001"

type checkpointFixture struct {
	store store.Store
	drv   driver.Driver
	idx   index.StoreIndex
	rec   *eventfx.Recorder
}

func newCheckpointFixture(t *testing.T) checkpointFixture {
	t.Helper()
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	return checkpointFixture{store: st, drv: drv, idx: idx, rec: rec}
}

func (f checkpointFixture) put(t *testing.T, data string) {
	t.Helper()
	if _, err := f.store.Put(context.Background(), artifactfx.Payload(data), domain.WithNamespace("s")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func (f checkpointFixture) checkpointNames(t *testing.T) []string {
	t.Helper()
	var names []string
	if err := f.store.System().Walk(context.Background(), "store.checkpoint.",
		func(name string, _ domain.Manifest) error {
			names = append(names, name)
			return nil
		}); err != nil {
		t.Fatalf("Walk checkpoints: %v", err)
	}
	return names
}

func newCheckpoint(t *testing.T, f checkpointFixture, cfg checkpoint.CheckpointConfig) checkpoint.CheckpointAgent {
	t.Helper()
	a, err := checkpoint.NewCheckpointAgent(f.store, f.drv, f.idx, f.rec, checkpointHostID, "store-snap", cfg)
	if err != nil {
		t.Fatalf("NewCheckpointAgent: %v", err)
	}
	return a
}

func TestNewCheckpoint_RequiresDeps(t *testing.T) {
	f := newCheckpointFixture(t)
	cases := map[string]func() (checkpoint.CheckpointAgent, error){
		"nil store": func() (checkpoint.CheckpointAgent, error) {
			return checkpoint.NewCheckpointAgent(nil, f.drv, f.idx, f.rec, checkpointHostID, "", checkpoint.CheckpointConfig{})
		},
		"nil driver": func() (checkpoint.CheckpointAgent, error) {
			return checkpoint.NewCheckpointAgent(f.store, nil, f.idx, f.rec, checkpointHostID, "", checkpoint.CheckpointConfig{})
		},
		"nil index": func() (checkpoint.CheckpointAgent, error) {
			return checkpoint.NewCheckpointAgent(f.store, f.drv, nil, f.rec, checkpointHostID, "", checkpoint.CheckpointConfig{})
		},
		"nil bus": func() (checkpoint.CheckpointAgent, error) {
			return checkpoint.NewCheckpointAgent(f.store, f.drv, f.idx, nil, checkpointHostID, "", checkpoint.CheckpointConfig{})
		},
		"empty host": func() (checkpoint.CheckpointAgent, error) {
			return checkpoint.NewCheckpointAgent(f.store, f.drv, f.idx, f.rec, "", "", checkpoint.CheckpointConfig{})
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

func TestCheckpoint_TakeCheckpoint_PublishesToCAS(t *testing.T) {
	f := newCheckpointFixture(t)
	f.put(t, "artifact one")
	f.put(t, "artifact two")

	a := newCheckpoint(t, f, checkpoint.CheckpointConfig{})
	stats, err := a.TakeCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("TakeCheckpoint: %v", err)
	}
	if stats.CheckpointID == "" {
		t.Error("empty CheckpointID")
	}
	if stats.DBBytes <= 0 {
		t.Errorf("DBBytes = %d, want > 0", stats.DBBytes)
	}

	// Exactly one checkpoint now in the CAS, readable and non-empty.
	names := f.checkpointNames(t)
	if len(names) != 1 {
		t.Fatalf("checkpoint count = %d, want 1", len(names))
	}
	rh, err := f.store.System().Get(context.Background(), names[0])
	if err != nil {
		t.Fatalf("System().Get(%s): %v", names[0], err)
	}
	defer rh.Close()
	body, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	// A valid SQLite file begins with the "SQLite format 3\000" header.
	if !strings.HasPrefix(string(body), "SQLite format 3") {
		t.Errorf("checkpoint body is not a SQLite database (got %d bytes, prefix %q)",
			len(body), firstN(body, 16))
	}
}

func TestCheckpoint_RetentionPrunesOldest(t *testing.T) {
	f := newCheckpointFixture(t)

	a := newCheckpoint(t, f, checkpoint.CheckpointConfig{Retention: 2})
	// Three checkpoints, each preceded by a fresh Put so the index — and
	// therefore the vacuumed bytes and the checkpoint's ArtifactID —
	// differ. Identical checkpoints would dedup onto one CAS artifact
	// (ADR-58), and deleting one name would drop the artifact shared by
	// the others; varying content keeps each checkpoint independent, the
	// way real checkpoints over a changing index are.
	var ids []string
	for i := 0; i < 3; i++ {
		f.put(t, "payload variation "+string(rune('A'+i)))
		st, err := a.TakeCheckpoint(context.Background())
		if err != nil {
			t.Fatalf("TakeCheckpoint #%d: %v", i, err)
		}
		ids = append(ids, st.CheckpointID)
	}

	names := f.checkpointNames(t)
	if len(names) != 2 {
		t.Fatalf("after Retention=2 over 3 checkpoints: count = %d, want 2", len(names))
	}
	// The oldest (ids[0]) must be gone; the newest (ids[2]) must remain.
	for _, n := range names {
		if strings.HasSuffix(n, ids[0]) {
			t.Errorf("oldest checkpoint %s should have been pruned", ids[0])
		}
	}
}

func TestCheckpoint_BlockedByForeignLease(t *testing.T) {
	f := newCheckpointFixture(t)
	f.put(t, "payload")
	leasefx.StageForeign(t, f.drv, "store.state.checkpoint.lease", "other-host", "Checkpoint", time.Hour)
	a := newCheckpoint(t, f, checkpoint.CheckpointConfig{})
	if _, err := a.TakeCheckpoint(context.Background()); err == nil {
		t.Fatal("TakeCheckpoint with a live foreign lease = nil, want lease-held failure")
	}
}

func TestCheckpoint_CancelledContext(t *testing.T) {
	f := newCheckpointFixture(t)
	f.put(t, "payload")
	a := newCheckpoint(t, f, checkpoint.CheckpointConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.TakeCheckpoint(ctx); err == nil {
		t.Fatal("TakeCheckpoint(cancelled) = nil, want error")
	}
}

func TestCheckpoint_RunStopsOnContextCancel(t *testing.T) {
	f := newCheckpointFixture(t)
	a := newCheckpoint(t, f, checkpoint.CheckpointConfig{Interval: time.Hour})
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

func firstN(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}

package agent_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

const snapshotHostID = "snapshot-host-0001"

type snapshotFixture struct {
	store store.Store
	drv   driver.Driver
	idx   index.StoreIndex
	rec   *eventfx.Recorder
}

func newSnapshotFixture(t *testing.T) snapshotFixture {
	t.Helper()
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	return snapshotFixture{store: st, drv: drv, idx: idx, rec: rec}
}

func (f snapshotFixture) put(t *testing.T, data string) {
	t.Helper()
	if _, err := f.store.Put(context.Background(), artifactfx.Payload(data), store.WithNamespace("s")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func (f snapshotFixture) snapshotNames(t *testing.T) []string {
	t.Helper()
	var names []string
	if err := f.store.System().Walk(context.Background(), "index_snapshot/",
		func(name string, _ domain.Manifest) error {
			names = append(names, name)
			return nil
		}); err != nil {
		t.Fatalf("Walk snapshots: %v", err)
	}
	return names
}

func newSnapshot(t *testing.T, f snapshotFixture, cfg agent.SnapshotConfig) agent.SnapshotAgent {
	t.Helper()
	a, err := agent.NewSnapshotAgent(f.store, f.drv, f.idx, f.rec, snapshotHostID, "store-snap", cfg)
	if err != nil {
		t.Fatalf("NewSnapshotAgent: %v", err)
	}
	return a
}

func TestNewSnapshot_RequiresDeps(t *testing.T) {
	f := newSnapshotFixture(t)
	cases := map[string]func() (agent.SnapshotAgent, error){
		"nil store": func() (agent.SnapshotAgent, error) {
			return agent.NewSnapshotAgent(nil, f.drv, f.idx, f.rec, snapshotHostID, "", agent.SnapshotConfig{})
		},
		"nil driver": func() (agent.SnapshotAgent, error) {
			return agent.NewSnapshotAgent(f.store, nil, f.idx, f.rec, snapshotHostID, "", agent.SnapshotConfig{})
		},
		"nil index": func() (agent.SnapshotAgent, error) {
			return agent.NewSnapshotAgent(f.store, f.drv, nil, f.rec, snapshotHostID, "", agent.SnapshotConfig{})
		},
		"nil bus": func() (agent.SnapshotAgent, error) {
			return agent.NewSnapshotAgent(f.store, f.drv, f.idx, nil, snapshotHostID, "", agent.SnapshotConfig{})
		},
		"empty host": func() (agent.SnapshotAgent, error) {
			return agent.NewSnapshotAgent(f.store, f.drv, f.idx, f.rec, "", "", agent.SnapshotConfig{})
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

func TestSnapshot_TakeSnapshot_PublishesToCAS(t *testing.T) {
	f := newSnapshotFixture(t)
	f.put(t, "artifact one")
	f.put(t, "artifact two")

	a := newSnapshot(t, f, agent.SnapshotConfig{})
	stats, err := a.TakeSnapshot(context.Background())
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}
	if stats.SnapshotID == "" {
		t.Error("empty SnapshotID")
	}
	if stats.DBBytes <= 0 {
		t.Errorf("DBBytes = %d, want > 0", stats.DBBytes)
	}

	// Exactly one snapshot now in the CAS, readable and non-empty.
	names := f.snapshotNames(t)
	if len(names) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(names))
	}
	rh, err := f.store.System().Get(context.Background(), names[0])
	if err != nil {
		t.Fatalf("System().Get(%s): %v", names[0], err)
	}
	defer rh.Close()
	body, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	// A valid SQLite file begins with the "SQLite format 3\000" header.
	if !strings.HasPrefix(string(body), "SQLite format 3") {
		t.Errorf("snapshot body is not a SQLite database (got %d bytes, prefix %q)",
			len(body), firstN(body, 16))
	}
}

func TestSnapshot_RetentionPrunesOldest(t *testing.T) {
	f := newSnapshotFixture(t)

	a := newSnapshot(t, f, agent.SnapshotConfig{Retention: 2})
	// Three snapshots, each preceded by a fresh Put so the index — and
	// therefore the vacuumed bytes and the snapshot's ArtifactID —
	// differ. Identical snapshots would dedup onto one CAS artifact
	// (ADR-58), and deleting one name would drop the artifact shared by
	// the others; varying content keeps each snapshot independent, the
	// way real snapshots over a changing index are.
	var ids []string
	for i := 0; i < 3; i++ {
		f.put(t, "payload variation "+string(rune('A'+i)))
		st, err := a.TakeSnapshot(context.Background())
		if err != nil {
			t.Fatalf("TakeSnapshot #%d: %v", i, err)
		}
		ids = append(ids, st.SnapshotID)
	}

	names := f.snapshotNames(t)
	if len(names) != 2 {
		t.Fatalf("after Retention=2 over 3 snapshots: count = %d, want 2", len(names))
	}
	// The oldest (ids[0]) must be gone; the newest (ids[2]) must remain.
	for _, n := range names {
		if strings.HasSuffix(n, ids[0]) {
			t.Errorf("oldest snapshot %s should have been pruned", ids[0])
		}
	}
}

func TestSnapshot_BlockedByForeignLease(t *testing.T) {
	f := newSnapshotFixture(t)
	f.put(t, "payload")
	now := time.Now()
	rec := leaseRecordJSON("other-host", now, now.Add(time.Hour), "Snapshot")
	if err := f.drv.Put(context.Background(),
		"system.state/snapshot/lease", strings.NewReader(rec)); err != nil {
		t.Fatalf("stage lease: %v", err)
	}
	a := newSnapshot(t, f, agent.SnapshotConfig{})
	if _, err := a.TakeSnapshot(context.Background()); err == nil {
		t.Fatal("TakeSnapshot with a live foreign lease = nil, want lease-held failure")
	}
}

func TestSnapshot_CancelledContext(t *testing.T) {
	f := newSnapshotFixture(t)
	f.put(t, "payload")
	a := newSnapshot(t, f, agent.SnapshotConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.TakeSnapshot(ctx); err == nil {
		t.Fatal("TakeSnapshot(cancelled) = nil, want error")
	}
}

func TestSnapshot_RunStopsOnContextCancel(t *testing.T) {
	f := newSnapshotFixture(t)
	a := newSnapshot(t, f, agent.SnapshotConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if st, _ := a.Status(); st != agent.StateIdle {
		t.Errorf("state after stop = %v, want StateIdle", st)
	}
}

func firstN(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}

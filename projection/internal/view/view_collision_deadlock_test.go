package view_test

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/event"
	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/domain"
)

// A synchronous subscriber that reads the View from inside its
// EventPathCollision handler must not deadlock. Collision events are
// raised by Add/Move while the write lock is held, so the publish has to
// happen after the lock is released — otherwise the handler's RLock would
// block on the in-flight writer forever.
//
// eventfx.Recorder cannot catch this: its Subscribe is a no-op, so it
// never re-enters the View. This test uses the real synchronous bus.
func TestByPath_CollisionPublishDoesNotDeadlock(t *testing.T) {
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	// One owner backfilled at the shared path; the collision is then driven
	// live through Add, which is the locked publish path under test.
	src := projectionfx.New()
	src.Add(withCreatedAt("sha256-aaaa1111", "shared/path.txt", older), nil)

	bus := event.NewEventBus() // synchronous: Publish delivers inline
	v, err := vw.New(context.Background(), src,
		vw.WithProvidedViews(testProvided()),
		vw.WithEventBus(bus))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer v.Close()

	reentered := make(chan struct{}, 1)
	unsub := bus.Subscribe(func(e event.Event) {
		if e.Type != event.EventPathCollision {
			return
		}
		// Re-enter the View; StatsSnapshot takes v.mu.RLock(). On a
		// synchronous bus this runs on the same goroutine as Add.
		_ = v.StatsSnapshot()
		select {
		case reentered <- struct{}{}:
		default:
		}
	})
	defer unsub()

	done := make(chan error, 1)
	go func() {
		done <- v.Add(withCreatedAt("sha256-bbbb2222", "shared/path.txt", newer))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Add returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: Add did not return — collision event was published while holding the write lock")
	}

	select {
	case <-reentered:
	case <-time.After(time.Second):
		t.Fatal("collision handler never ran — re-entrant read was not exercised")
	}

	// Sanity: the collision actually happened and the fresher artifact won.
	if v.Stats.CollisionCount != 1 {
		t.Errorf("CollisionCount: got %d, want 1", v.Stats.CollisionCount)
	}
	n, err := v.GetIn(testRoot, "shared/path.txt")
	if err != nil {
		t.Fatalf("GetIn winner: %v", err)
	}
	if n.Artifact.ArtifactID != domain.ArtifactID("sha256-bbbb2222") {
		t.Errorf("winner: got %q, want sha256-bbbb2222", n.Artifact.ArtifactID)
	}
}

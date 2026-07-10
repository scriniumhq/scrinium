package view_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/projectionfx"
)

// Adding to the View from inside a ListIn yield must not deadlock. The
// iterator snapshots its nodes under the read lock and releases it before
// yielding, so Add's write lock is free to proceed. Before F2 the read
// lock was held across yield and this self-deadlocked on one goroutine.
func TestListIn_MutateDuringYieldDoesNotDeadlock(t *testing.T) {
	now := time.Now().UTC()
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "sess1", 100, now), nil)
	src.Add(makeManifest("sha256-eeffgghh", "sess1", 100, now), nil)

	v, err := vw.New(context.Background(), src)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer v.Close()

	done := make(chan error, 1)
	go func() {
		var count int
		for _, err := range v.ListIn(vw.RootByArtifact, "") {
			if err != nil {
				done <- err
				return
			}
			count++
			if count == 1 {
				if addErr := v.Add(makeManifest("sha256-99990000", "sess1", 50, now)); addErr != nil {
					done <- addErr
					return
				}
			}
		}
		if count == 0 {
			done <- fmt.Errorf("no nodes iterated")
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("iteration failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: ListIn held the read lock across yield while Add took the write lock")
	}

	// The mutation took effect; the in-flight iteration was over a detached
	// snapshot, so it neither saw nor was disturbed by the new artifact.
	if _, err := v.GetIn(vw.RootByArtifact, "99/99/sha256-99990000"); err != nil {
		t.Errorf("added artifact missing after iteration: %v", err)
	}
}

// Removing from the View from inside a WalkIn yield must not deadlock,
// for the same reason as the ListIn case above.
func TestWalkIn_RemoveDuringYieldDoesNotDeadlock(t *testing.T) {
	now := time.Now().UTC()
	src := projectionfx.New()
	src.Add(makeManifest("sha256-aabbccdd", "sess1", 100, now), nil)
	src.Add(makeManifest("sha256-aabb9999", "sess1", 100, now), nil)

	v, err := vw.New(context.Background(), src)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer v.Close()

	done := make(chan error, 1)
	go func() {
		removed := false
		for n, err := range v.WalkIn(vw.RootByArtifact, "") {
			if err != nil {
				done <- err
				return
			}
			if !removed && n.Artifact != nil {
				if rmErr := v.Remove(n.Artifact.ArtifactID); rmErr != nil {
					done <- rmErr
					return
				}
				removed = true
			}
		}
		if !removed {
			done <- fmt.Errorf("no artifact node encountered")
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("walk failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: WalkIn held the read lock across yield while Remove took the write lock")
	}
}

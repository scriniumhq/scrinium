// Coalesced-mode behaviour. The Unique default is covered by
// TestPut_SharedBlobAcrossArtifacts (identical content under distinct
// sessions → two artifacts sharing one blob). This file pins the
// opposite contract enabled by WithCoalesced.

package store_test

import (
	"context"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	storefx2 "scrinium.dev/testutil/storefx"
)

// TestPut_CoalescedMode_SameContentCoalesces: in IdentityModeCoalesced
// the per-Put nonce is omitted (ADR-73), so two Puts of identical
// content+identity derive the SAME handle and the second collapses into
// the first via ON CONFLICT(artifact_id) DO NOTHING — one artifact, one
// blob. This is the WORM-archive contract that distinguishes Coalesced
// from the Unique default, where the two Puts are distinct artifacts.
func TestPut_CoalescedMode_SameContentCoalesces(t *testing.T) {
	s := storefx2.Init(t, store.WithCoalesced())
	ctx := context.Background()

	id1, err := s.Put(ctx, payload("identical bytes"), domain.WithNamespace("arc"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(ctx, payload("identical bytes"), domain.WithNamespace("arc"))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("Coalesced: identical content must yield one handle, got %q vs %q", id1, id2)
	}

	info, err := s.Capacity(ctx)
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 1 {
		t.Errorf("artifact count: got %d, want 1 (coalesced)", info.ArtifactCount)
	}
	if info.BlobCount != 1 {
		t.Errorf("blob count: got %d, want 1", info.BlobCount)
	}

	var seen int
	if err := s.Walk(ctx, "arc", func(domain.Manifest) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("Walk: got %d manifests, want 1 (coalesced)", seen)
	}
}

// TestPut_CoalescedMode_DistinctContentStaysDistinct: coalescing keys on
// content — different content under the same identity must remain two
// separate artifacts even in Coalesced mode.
func TestPut_CoalescedMode_DistinctContentStaysDistinct(t *testing.T) {
	s := storefx2.Init(t, store.WithCoalesced())
	ctx := context.Background()

	idA, err := s.Put(ctx, payload("alpha"), domain.WithNamespace("arc"))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.Put(ctx, payload("beta"), domain.WithNamespace("arc"))
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Fatalf("distinct content must not coalesce, both = %q", idA)
	}

	info, err := s.Capacity(ctx)
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if info.ArtifactCount != 2 {
		t.Errorf("artifact count: got %d, want 2", info.ArtifactCount)
	}
}

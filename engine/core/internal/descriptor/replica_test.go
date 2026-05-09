package descriptor

import (
	"errors"
	"os"
	"testing"

	"github.com/rkurbatov/scrinium/engine/errs"
)

// d returns a fresh valid descriptor with the given Sequence.
// In-package tests reach the unexported helpers without aliasing.
func d(t *testing.T, seq uint64) *Descriptor {
	t.Helper()
	return &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      seq,
		DEK:           nil,
		DEKEncrypted:  false,
	}
}

// d2 returns a fresh valid descriptor with a DIFFERENT StoreID
// at the given Sequence. Used to fabricate split-brain (same
// sequence, different content).
func d2(t *testing.T, seq uint64) *Descriptor {
	t.Helper()
	return &Descriptor{
		StoreID:       "99999999-aaaa-bbbb-cccc-dddddddddddd",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      seq,
		DEK:           nil,
		DEKEncrypted:  false,
	}
}

// --- 3x3 status matrix ---

func TestReconcile_BothAbsent(t *testing.T) {
	_, err := Reconcile(nil, ReplicaAbsent, nil, ReplicaAbsent)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestReconcile_AbsentCorrupted_BothUnrecoverable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		l0s, l1s ReplicaStatus
	}{
		{"absent_corrupted", ReplicaAbsent, ReplicaCorrupted},
		{"corrupted_absent", ReplicaCorrupted, ReplicaAbsent},
		{"corrupted_corrupted", ReplicaCorrupted, ReplicaCorrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Reconcile(nil, tc.l0s, nil, tc.l1s)
			if !errors.Is(err, errs.ErrStoreCorrupted) {
				t.Fatalf("expected ErrStoreCorrupted, got %v", err)
			}
		})
	}
}

func TestReconcile_L0Absent_L1Valid_HealsL0(t *testing.T) {
	l1 := d(t, 5)
	r, err := Reconcile(nil, ReplicaAbsent, l1, ReplicaValid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealL0FromL1 {
		t.Errorf("Action: got %v, want HealL0FromL1", r.Action)
	}
	if r.Canonical != l1 {
		t.Error("Canonical should be L1")
	}
}

func TestReconcile_L0Corrupted_L1Valid_HealsL0(t *testing.T) {
	l1 := d(t, 5)
	r, err := Reconcile(nil, ReplicaCorrupted, l1, ReplicaValid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealL0FromL1 {
		t.Errorf("Action: got %v, want HealL0FromL1", r.Action)
	}
}

func TestReconcile_L0Valid_L1Absent_HealsL1(t *testing.T) {
	l0 := d(t, 5)
	r, err := Reconcile(l0, ReplicaValid, nil, ReplicaAbsent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealL1FromL0 {
		t.Errorf("Action: got %v, want HealL1FromL0", r.Action)
	}
	if r.Canonical != l0 {
		t.Error("Canonical should be L0")
	}
}

func TestReconcile_L0Valid_L1Corrupted_HealsL1(t *testing.T) {
	l0 := d(t, 5)
	r, err := Reconcile(l0, ReplicaValid, nil, ReplicaCorrupted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealL1FromL0 {
		t.Errorf("Action: got %v, want HealL1FromL0", r.Action)
	}
}

// --- Both valid, equal ---

func TestReconcile_BothValid_Equal_NoHeal(t *testing.T) {
	l0 := d(t, 5)
	l1 := d(t, 5)
	r, err := Reconcile(l0, ReplicaValid, l1, ReplicaValid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealNone {
		t.Errorf("Action: got %v, want HealNone", r.Action)
	}
	if r.Canonical != l0 {
		t.Error("Canonical should be L0 (when equal)")
	}
}

// --- Both valid, different sequences ---

func TestReconcile_BothValid_L0Newer_RepairsL1(t *testing.T) {
	l0 := d(t, 7)
	l1 := d(t, 3)
	r, err := Reconcile(l0, ReplicaValid, l1, ReplicaValid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealBothFromL0 {
		t.Errorf("Action: got %v, want HealBothFromL0", r.Action)
	}
	if r.Canonical != l0 {
		t.Error("Canonical should be L0 (higher sequence)")
	}
}

func TestReconcile_BothValid_L1Newer_RepairsL0(t *testing.T) {
	l0 := d(t, 3)
	l1 := d(t, 7)
	r, err := Reconcile(l0, ReplicaValid, l1, ReplicaValid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealBothFromL1 {
		t.Errorf("Action: got %v, want HealBothFromL1", r.Action)
	}
	if r.Canonical != l1 {
		t.Error("Canonical should be L1 (higher sequence)")
	}
}

// --- Split-brain ---

func TestReconcile_SplitBrain_SameSequenceDifferentContent(t *testing.T) {
	l0 := d(t, 5)
	l1 := d2(t, 5) // same sequence, different StoreID
	_, err := Reconcile(l0, ReplicaValid, l1, ReplicaValid)
	if !errors.Is(err, errs.ErrDescriptorSplitBrain) {
		t.Fatalf("expected ErrDescriptorSplitBrain, got %v", err)
	}
}

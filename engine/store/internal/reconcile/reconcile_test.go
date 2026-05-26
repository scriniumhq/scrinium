package reconcile

import (
	"errors"
	"os"
	"testing"

	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
)

func d(t *testing.T, seq uint64) *descriptor.Descriptor {
	t.Helper()
	return &descriptor.Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: descriptor.CurrentSchemaVersion,
		Sequence:      seq,
	}
}

// d2 differs from d only in StoreID, to fabricate split-brain.
func d2(t *testing.T, seq uint64) *descriptor.Descriptor {
	t.Helper()
	return &descriptor.Descriptor{
		StoreID:       "99999999-aaaa-bbbb-cccc-dddddddddddd",
		SchemaVersion: descriptor.CurrentSchemaVersion,
		Sequence:      seq,
	}
}

func TestReconcile_BothAbsent(t *testing.T) {
	_, err := Reconcile(nil, Absent, nil, Absent)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestReconcile_AbsentCorrupted_BothUnrecoverable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		l0s, l1s Status
	}{
		{"absent_corrupted", Absent, Corrupted},
		{"corrupted_absent", Corrupted, Absent},
		{"corrupted_corrupted", Corrupted, Corrupted},
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
	r, err := Reconcile(nil, Absent, l1, Valid)
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
	r, err := Reconcile(nil, Corrupted, l1, Valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealL0FromL1 {
		t.Errorf("Action: got %v, want HealL0FromL1", r.Action)
	}
}

func TestReconcile_L0Valid_L1Absent_HealsL1(t *testing.T) {
	l0 := d(t, 5)
	r, err := Reconcile(l0, Valid, nil, Absent)
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
	r, err := Reconcile(l0, Valid, nil, Corrupted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Action != HealL1FromL0 {
		t.Errorf("Action: got %v, want HealL1FromL0", r.Action)
	}
}

func TestReconcile_BothValid_Equal_NoHeal(t *testing.T) {
	l0 := d(t, 5)
	l1 := d(t, 5)
	r, err := Reconcile(l0, Valid, l1, Valid)
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

func TestReconcile_BothValid_L0Newer_RepairsL1(t *testing.T) {
	l0 := d(t, 7)
	l1 := d(t, 3)
	r, err := Reconcile(l0, Valid, l1, Valid)
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
	r, err := Reconcile(l0, Valid, l1, Valid)
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

func TestReconcile_SplitBrain_SameSequenceDifferentContent(t *testing.T) {
	l0 := d(t, 5)
	l1 := d2(t, 5)
	_, err := Reconcile(l0, Valid, l1, Valid)
	if !errors.Is(err, errs.ErrDescriptorSplitBrain) {
		t.Fatalf("expected ErrDescriptorSplitBrain, got %v", err)
	}
}

package reconcile

import (
	"context"
	"errors"
	"os"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/named"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
)

var testHashes = descriptor.CanonicalHashes()

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

// --- ReadReplica / ReadBoth: on-disk read + corruption classification ----
//
// The Reconcile tests above run on constructed (descriptor, Status) pairs.
// These cover the layer that PRODUCES those pairs from the driver — the
// part that decides Valid / Absent / Corrupted from raw bytes — which the
// pure-decision tests never touch.

func writeReplica(t *testing.T, drv driver.Driver, r descriptor.Replica, desc *descriptor.Descriptor) {
	t.Helper()
	if err := descriptor.WriteReplica(context.Background(), drv, testHashes, desc, r); err != nil {
		t.Fatalf("WriteReplica(%v): %v", r, err)
	}
}

// corruptCell writes a valid Plain manifest whose inline payload is not a
// descriptor, so LoadCell succeeds (hash matches) but Unmarshal fails —
// a deterministic Corrupted classification via the parse branch.
func corruptCell(t *testing.T, drv driver.Driver, r descriptor.Replica) {
	t.Helper()
	name, err := r.Name()
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	body, _, err := named.BuildInlineManifest(name, []byte("}{ not a descriptor"), string(domain.HashSHA256), testHashes, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatalf("build corrupt manifest: %v", err)
	}
	if err := named.WriteCell(context.Background(), drv, name, body, false); err != nil {
		t.Fatalf("write corrupt cell: %v", err)
	}
}

func TestReadReplica_Valid(t *testing.T) {
	drv := driverfx.LocalFS(t)
	writeReplica(t, drv, descriptor.L0, d(t, 5))

	got, status, err := ReadReplica(context.Background(), drv, testHashes, descriptor.L0)
	if err != nil {
		t.Fatalf("ReadReplica: %v", err)
	}
	if status != Valid {
		t.Errorf("status: got %v, want Valid", status)
	}
	if got == nil || got.Sequence != 5 {
		t.Errorf("descriptor: got %+v, want Sequence=5", got)
	}
}

func TestReadReplica_Absent(t *testing.T) {
	drv := driverfx.LocalFS(t)
	got, status, err := ReadReplica(context.Background(), drv, testHashes, descriptor.L0)
	if err != nil {
		t.Fatalf("ReadReplica on a missing file must not error: %v", err)
	}
	if status != Absent {
		t.Errorf("status: got %v, want Absent", status)
	}
	if got != nil {
		t.Errorf("descriptor: got %+v, want nil", got)
	}
}

func TestReadReplica_Corrupted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	corruptCell(t, drv, descriptor.L0)
	got, status, err := ReadReplica(context.Background(), drv, testHashes, descriptor.L0)
	if status != Corrupted {
		t.Errorf("status: got %v, want Corrupted", status)
	}
	if err == nil {
		t.Error("a Corrupted read should carry a diagnostic error")
	}
	if got != nil {
		t.Errorf("descriptor: got %+v, want nil", got)
	}
}

func TestReadBoth_BothValid(t *testing.T) {
	drv := driverfx.LocalFS(t)
	writeReplica(t, drv, descriptor.L0, d(t, 5))
	writeReplica(t, drv, descriptor.L1, d(t, 5))

	l0, l1, l0s, l1s, err := ReadBoth(context.Background(), drv, testHashes)
	if err != nil {
		t.Fatalf("ReadBoth: %v", err)
	}
	if l0s != Valid || l1s != Valid {
		t.Errorf("statuses: got L0=%v L1=%v, want Valid/Valid", l0s, l1s)
	}
	if l0 == nil || l1 == nil {
		t.Error("both descriptors should be non-nil when both valid")
	}
}

func TestReadBoth_L0Valid_L1Absent(t *testing.T) {
	drv := driverfx.LocalFS(t)
	writeReplica(t, drv, descriptor.L0, d(t, 5)) // L1 (backup replica) left missing

	l0, l1, l0s, l1s, err := ReadBoth(context.Background(), drv, testHashes)
	if err != nil {
		t.Fatalf("ReadBoth: %v", err)
	}
	if l0s != Valid {
		t.Errorf("L0 status: got %v, want Valid", l0s)
	}
	if l1s != Absent {
		t.Errorf("L1 status: got %v, want Absent", l1s)
	}
	if l0 == nil {
		t.Error("L0 should be non-nil")
	}
	if l1 != nil {
		t.Errorf("L1 should be nil when absent, got %+v", l1)
	}
}

// TestReadBoth_L0Corrupted_L1Valid confirms per-replica corruption is
// reported through the status, NOT as a returned error (only a
// non-ErrNotExist I/O failure is a returned error).
func TestReadBoth_L0Corrupted_L1Valid(t *testing.T) {
	drv := driverfx.LocalFS(t)
	corruptCell(t, drv, descriptor.L0)
	writeReplica(t, drv, descriptor.L1, d(t, 9))

	_, l1, l0s, l1s, err := ReadBoth(context.Background(), drv, testHashes)
	if err != nil {
		t.Fatalf("ReadBoth must not return per-replica corruption as an error: %v", err)
	}
	if l0s != Corrupted {
		t.Errorf("L0 status: got %v, want Corrupted", l0s)
	}
	if l1s != Valid || l1 == nil {
		t.Errorf("L1: got status=%v desc=%+v, want Valid/non-nil", l1s, l1)
	}
}

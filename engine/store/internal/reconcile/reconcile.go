// Package reconcile reads the two descriptor replicas and decides
// which is canonical and what repair (if any) the caller must perform.
// It is the recovery-decision machine over the descriptor's on-disk
// shape; the descriptor package owns the shape, (de)serialisation, and
// the two-replica cell write that this package's callers use to heal.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/named"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
)

// Status is the outcome of attempting to read one descriptor replica.
type Status int

const (
	// Absent — the cell does not exist (errs.ErrArtifactNotFound).
	Absent Status = iota
	// Corrupted — the cell exists but failed verify, Unmarshal, or Validate.
	Corrupted
	// Valid — the descriptor parsed and validated.
	Valid
)

// String returns the lowercase status name.
func (s Status) String() string {
	switch s {
	case Absent:
		return "absent"
	case Corrupted:
		return "corrupted"
	case Valid:
		return "valid"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Action tells the caller what repair to perform after Reconcile picks
// the canonical descriptor.
type Action int

const (
	// HealNone — both replicas valid and equal; no action.
	HealNone Action = iota
	// HealL0FromL1 — L0 missing/corrupt; rewrite it from canonical (L1).
	HealL0FromL1
	// HealL1FromL0 — L1 missing/corrupt; rewrite it from canonical (L0).
	HealL1FromL0
	// HealBothFromL0 — replicas diverged and L0 won; rewrite L1.
	HealBothFromL0
	// HealBothFromL1 — replicas diverged and L1 won; rewrite L0.
	HealBothFromL1
)

// String returns the snake_case action name.
func (a Action) String() string {
	switch a {
	case HealNone:
		return "none"
	case HealL0FromL1:
		return "heal_L0_from_L1"
	case HealL1FromL0:
		return "heal_L1_from_L0"
	case HealBothFromL0:
		return "repair_L1_from_L0"
	case HealBothFromL1:
		return "repair_L0_from_L1"
	default:
		return fmt.Sprintf("Action(%d)", int(a))
	}
}

// Result is the outcome of Reconcile: the chosen authoritative
// descriptor and the repair the caller should perform.
type Result struct {
	Canonical *descriptor.Descriptor
	Action    Action
	L0Status  Status
	L1Status  Status
}

// ReadReplica reads one descriptor replica cell through the named layer
// (verify-on-read). Returns (d, Valid, nil) on a clean read;
// (nil, Absent, nil) when the cell is missing; (nil, Corrupted, err)
// when it exists but fails verify/parse (err is diagnostic; Corrupted is
// reconcilable); and (nil, Absent, err) on any other I/O failure (propagate).
func ReadReplica(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, r descriptor.Replica) (*descriptor.Descriptor, Status, error) {
	name, err := r.Name()
	if err != nil {
		return nil, Absent, fmt.Errorf("reconcile.ReadReplica: %w", err)
	}
	m, err := named.LoadCell(ctx, drv, hashes, name)
	if err != nil {
		switch {
		case errors.Is(err, errs.ErrArtifactNotFound):
			return nil, Absent, nil
		case errors.Is(err, errs.ErrCorruptedContent):
			return nil, Corrupted, fmt.Errorf("reconcile.ReadReplica %s: verify: %w", r, err)
		default:
			return nil, Absent, err
		}
	}
	d, err := descriptor.Unmarshal(m.InlineBlob)
	if err != nil {
		return nil, Corrupted, fmt.Errorf("reconcile.ReadReplica %s: parse: %w", r, err)
	}
	return d, Valid, nil
}

// ReadBoth reads both replicas. Per-replica corruption is reported
// through the status; only a hard (non-not-found) I/O failure is returned
// as err (with the matching descriptor nil and status Absent).
func ReadBoth(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry) (l0, l1 *descriptor.Descriptor, l0s, l1s Status, err error) {
	l0, l0s, err0 := ReadReplica(ctx, drv, hashes, descriptor.L0)
	l1, l1s, err1 := ReadReplica(ctx, drv, hashes, descriptor.L1)

	if l0s == Absent && err0 != nil {
		return nil, nil, l0s, l1s, err0
	}
	if l1s == Absent && err1 != nil {
		return nil, nil, l0s, l1s, err1
	}
	return l0, l1, l0s, l1s, nil
}

// Reconcile picks the canonical descriptor from a replica pair and
// decides the repair. Location is the source of truth.
//
//   - both absent              → os.ErrNotExist (caller distinguishes
//     fresh Location from corrupted Store).
//   - both invalid / one each  → errs.ErrStoreCorrupted.
//   - one valid                → heal the other from it.
//   - both valid, equal        → HealNone.
//   - both valid, diverged     → higher sequence wins, repair the loser;
//     equal sequence → errs.ErrDescriptorSplitBrain.
func Reconcile(l0 *descriptor.Descriptor, l0s Status, l1 *descriptor.Descriptor, l1s Status) (Result, error) {
	switch {
	case l0s == Absent && l1s == Absent:
		return Result{L0Status: l0s, L1Status: l1s}, os.ErrNotExist

	case l0s != Valid && l1s != Valid:
		return Result{L0Status: l0s, L1Status: l1s}, errs.ErrStoreCorrupted

	case l0s != Valid && l1s == Valid:
		return Result{Canonical: l1, Action: HealL0FromL1, L0Status: l0s, L1Status: l1s}, nil

	case l0s == Valid && l1s != Valid:
		return Result{Canonical: l0, Action: HealL1FromL0, L0Status: l0s, L1Status: l1s}, nil

	default:
		if descriptor.Equal(l0, l1) {
			return Result{Canonical: l0, Action: HealNone, L0Status: l0s, L1Status: l1s}, nil
		}
		switch {
		case l0.Sequence > l1.Sequence:
			return Result{Canonical: l0, Action: HealBothFromL0, L0Status: l0s, L1Status: l1s}, nil
		case l1.Sequence > l0.Sequence:
			return Result{Canonical: l1, Action: HealBothFromL1, L0Status: l0s, L1Status: l1s}, nil
		default:
			return Result{L0Status: l0s, L1Status: l1s}, errs.ErrDescriptorSplitBrain
		}
	}
}

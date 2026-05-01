package descriptor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/errs"
)

// BackupPath is the L1 shadow-copy descriptor location relative
// to the driver root. Per §10.1.5 the backup is written
// synchronously with the primary on every descriptor mutation.
const BackupPath = ".store.backup.json"

// Replica identifies one of the two on-disk descriptor copies.
// Used by WriteReplica and by Reconcile-driven self-heal to
// name which side is being targeted.
type Replica int

const (
	// L0 — the primary descriptor (store.json).
	L0 Replica = iota

	// L1 — the shadow descriptor (.store.backup.json).
	L1
)

// String returns the spec-canonical short name of the replica
// ("L0" or "L1"). Used in error wrapping and debug logging.
func (r Replica) String() string {
	switch r {
	case L0:
		return "L0"
	case L1:
		return "L1"
	default:
		return fmt.Sprintf("Replica(%d)", int(r))
	}
}

// Path returns the on-disk path of the replica relative to the
// driver root. Returns an error for an out-of-range Replica
// value to make typo'd casts (Replica(2)) loud rather than
// silently writing to "".
func (r Replica) Path() (string, error) {
	switch r {
	case L0:
		return Path, nil
	case L1:
		return BackupPath, nil
	default:
		return "", fmt.Errorf("descriptor: unknown Replica %d", int(r))
	}
}

// ReplicaStatus is the outcome of attempting to read one
// descriptor replica from a Driver.
type ReplicaStatus int

const (
	// ReplicaAbsent — Driver returned os.ErrNotExist; the file
	// genuinely does not exist (vs unreadable for other reasons).
	ReplicaAbsent ReplicaStatus = iota

	// ReplicaCorrupted — file exists but Unmarshal or Validate
	// rejected its content. The file is on disk; its bytes are
	// not a valid descriptor.
	ReplicaCorrupted

	// ReplicaValid — the descriptor parsed and validated.
	ReplicaValid
)

func (s ReplicaStatus) String() string {
	switch s {
	case ReplicaAbsent:
		return "absent"
	case ReplicaCorrupted:
		return "corrupted"
	case ReplicaValid:
		return "valid"
	default:
		return fmt.Sprintf("ReplicaStatus(%d)", int(s))
	}
}

// ReadReplica reads one descriptor replica through the Driver.
// The path is BackupPath for L1, Path for L0; the function
// itself is replica-agnostic.
//
// Returns:
//   - (d, ReplicaValid, nil)     — clean read.
//   - (nil, ReplicaAbsent, nil)  — file does not exist.
//   - (nil, ReplicaCorrupted, descriptiveErr) — file exists but
//     parsed or validated badly. The error is non-nil to give
//     diagnostics, but ReplicaCorrupted is the reconcilable
//     condition; callers should treat the error as informational
//     and let Reconcile decide whether to recover.
//   - (nil, ReplicaAbsent, ioErr) — I/O failure other than
//     ErrNotExist; not reconcilable, propagate.
func ReadReplica(ctx context.Context, drv driver.Driver, path string) (*Descriptor, ReplicaStatus, error) {
	rc, err := drv.Get(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ReplicaAbsent, nil
		}
		return nil, ReplicaAbsent, err
	}
	defer rc.Close()

	data, err := readAll(rc)
	if err != nil {
		return nil, ReplicaCorrupted, fmt.Errorf("descriptor.ReadReplica %q: read: %w", path, err)
	}
	d, err := Unmarshal(data)
	if err != nil {
		return nil, ReplicaCorrupted, fmt.Errorf("descriptor.ReadReplica %q: parse: %w", path, err)
	}
	return d, ReplicaValid, nil
}

// ReadBoth reads both replicas in sequence. Either may fail
// independently; ReadBoth combines the outcomes for Reconcile.
// I/O errors that are not "file not found" are propagated; in
// that case the corresponding *Descriptor is nil and status is
// ReplicaAbsent. Per-replica corruption is reported through the
// status, not through the returned error.
func ReadBoth(ctx context.Context, drv driver.Driver) (l0, l1 *Descriptor, l0s, l1s ReplicaStatus, err error) {
	l0, l0s, err0 := ReadReplica(ctx, drv, Path)
	l1, l1s, err1 := ReadReplica(ctx, drv, BackupPath)

	// Treat "corrupted file" as a recoverable condition (Reconcile
	// will heal from the other replica). Non-recoverable I/O
	// errors (permission denied, network gone) propagate with
	// the first one wins.
	if l0s == ReplicaAbsent && err0 != nil {
		return nil, nil, l0s, l1s, err0
	}
	if l1s == ReplicaAbsent && err1 != nil {
		return nil, nil, l0s, l1s, err1
	}
	return l0, l1, l0s, l1s, nil
}

// ReconcileAction tells the caller what disk-level recovery to
// perform after Reconcile picks a canonical descriptor.
type ReconcileAction int

const (
	// HealNone — both replicas valid and equal. No action.
	HealNone ReconcileAction = iota

	// HealL0FromL1 — L0 missing or corrupted; rewrite L0 from
	// the canonical (which is L1's content).
	HealL0FromL1

	// HealL1FromL0 — L1 missing or corrupted; rewrite L1 from
	// the canonical (which is L0's content).
	HealL1FromL0

	// HealBothFromL0 — replicas had different sequences and L0
	// won; L1 still needs to be rewritten with the canonical.
	// Operationally identical to HealL1FromL0; kept distinct so
	// callers can log "sequence-divergence repair" separately.
	HealBothFromL0

	// HealBothFromL1 — same as HealBothFromL0 but L1 won.
	HealBothFromL1
)

func (a ReconcileAction) String() string {
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
		return fmt.Sprintf("ReconcileAction(%d)", int(a))
	}
}

// ReconcileResult is the outcome of Reconcile: the chosen
// authoritative descriptor and a description of what disk-level
// repair (if any) the caller should perform.
type ReconcileResult struct {
	Canonical *Descriptor
	Action    ReconcileAction
	L0Status  ReplicaStatus
	L1Status  ReplicaStatus
}

// Reconcile picks the canonical descriptor from a pair of
// replicas and decides what (if any) repair is needed.
//
// Algorithm per §10.1.5 — Location is the source of truth, L2
// (store_meta) is not consulted here. The matrix is:
//
//	L0 \ L1  | Absent     Corrupted   Valid
//	---------+----------------------------------------
//	Absent   | os.ErrNot  Corrupted   HealL0FromL1
//	Corrupt  | Corrupted  Corrupted   HealL0FromL1
//	Valid    | HealL1F0   HealL1F0    Equal? sequence?
//
// When both replicas are valid:
//   - equal content       → HealNone.
//   - different content,
//     different sequence  → higher-sequence wins, repair the loser.
//   - different content,
//     same sequence       → split-brain; ErrDescriptorSplitBrain.
//
// "Both replicas absent" returns os.ErrNotExist — distinguishing
// "fresh Location" from "corrupted Store" is the caller's job
// (InitStore vs OpenStore make this distinction differently).
//
// "Both replicas corrupted (or one corrupted, one absent)"
// returns errs.ErrStoreCorrupted. Recovery requires either a
// snapshot (RebuildIndexAgent) or a Recovery Kit.
func Reconcile(l0 *Descriptor, l0s ReplicaStatus, l1 *Descriptor, l1s ReplicaStatus) (ReconcileResult, error) {
	switch {
	// Both gone — fresh Location case.
	case l0s == ReplicaAbsent && l1s == ReplicaAbsent:
		return ReconcileResult{L0Status: l0s, L1Status: l1s}, os.ErrNotExist

	// Both broken or one broken + one absent — unrecoverable.
	case l0s != ReplicaValid && l1s != ReplicaValid:
		return ReconcileResult{L0Status: l0s, L1Status: l1s}, errs.ErrStoreCorrupted

	// Only L1 valid — heal L0 from L1.
	case l0s != ReplicaValid && l1s == ReplicaValid:
		return ReconcileResult{
			Canonical: l1,
			Action:    HealL0FromL1,
			L0Status:  l0s,
			L1Status:  l1s,
		}, nil

	// Only L0 valid — heal L1 from L0.
	case l0s == ReplicaValid && l1s != ReplicaValid:
		return ReconcileResult{
			Canonical: l0,
			Action:    HealL1FromL0,
			L0Status:  l0s,
			L1Status:  l1s,
		}, nil

	// Both valid. Compare.
	default:
		if Equal(l0, l1) {
			return ReconcileResult{
				Canonical: l0,
				Action:    HealNone,
				L0Status:  l0s,
				L1Status:  l1s,
			}, nil
		}
		// Diverged. Sequence is the tiebreaker.
		switch {
		case l0.Sequence > l1.Sequence:
			return ReconcileResult{
				Canonical: l0,
				Action:    HealBothFromL0,
				L0Status:  l0s,
				L1Status:  l1s,
			}, nil
		case l1.Sequence > l0.Sequence:
			return ReconcileResult{
				Canonical: l1,
				Action:    HealBothFromL1,
				L0Status:  l0s,
				L1Status:  l1s,
			}, nil
		default:
			// Same sequence, different content — split-brain.
			// The engine cannot pick a winner; refuse to open.
			return ReconcileResult{L0Status: l0s, L1Status: l1s},
				errs.ErrDescriptorSplitBrain
		}
	}
}

// readAll reads rc fully with an upper bound. The descriptor is
// at most a few hundred bytes; a multi-megabyte read is a sign
// of corrupted I/O or an attack.
func readAll(rc io.Reader) ([]byte, error) {
	const maxDescriptorSize = 64 * 1024
	lr := io.LimitReader(rc, maxDescriptorSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(data) > maxDescriptorSize {
		return nil, fmt.Errorf("descriptor too large (>%d bytes)", maxDescriptorSize)
	}
	return data, nil
}

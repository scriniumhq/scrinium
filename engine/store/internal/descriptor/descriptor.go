package descriptor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"scrinium.dev/engine/driver"
)

// CurrentSchemaVersion is the schema version this build writes. A
// descriptor with a higher schema_version must be refused.
const CurrentSchemaVersion = 1

// Path is the L0 descriptor location relative to the driver root.
const Path = "store.json"

// BackupPath is the L1 shadow-copy descriptor location relative to
// the driver root, written synchronously with L0 on every mutation.
const BackupPath = ".store.backup.json"

// Descriptor is the on-disk shape of store.json: Store identity and
// the crypto material. Projection parameters live in system.config,
// not here.
type Descriptor struct {
	StoreID       string     `json:"store_id"`
	SchemaVersion int        `json:"schema_version"`
	Sequence      uint64     `json:"sequence"`
	DEK           []byte     `json:"dek"` // base64 in JSON; empty when DEKEncrypted=false
	DEKEncrypted  bool       `json:"dek_encrypted"`
	KDFParams     *KDFParams `json:"kdf_params,omitempty"`
}

// KDFParams describes the Argon2id parameters used to derive a KEK
// from a passphrase. Present only when DEKEncrypted=true.
type KDFParams struct {
	Algorithm string `json:"algorithm"`
	Time      uint32 `json:"time"`
	Memory    uint32 `json:"memory"`
	Threads   uint8  `json:"threads"`
	Salt      []byte `json:"salt"` // base64 in JSON
}

// Validate checks the descriptor for syntactic well-formedness.
// Cross-checks against external state are the caller's job.
func (d *Descriptor) Validate() error {
	if d.StoreID == "" {
		return errors.New("descriptor: empty store_id")
	}
	if d.SchemaVersion <= 0 {
		return fmt.Errorf("descriptor: invalid schema_version: %d", d.SchemaVersion)
	}
	if d.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("descriptor: schema_version %d exceeds supported %d",
			d.SchemaVersion, CurrentSchemaVersion)
	}
	if d.Sequence == 0 {
		return errors.New("descriptor: sequence must be >= 1")
	}
	if d.DEKEncrypted {
		if len(d.DEK) == 0 {
			return errors.New("descriptor: dek_encrypted=true but dek is empty")
		}
		if d.KDFParams == nil {
			return errors.New("descriptor: dek_encrypted=true but kdf_params is missing")
		}
	}
	return nil
}

// Marshal serialises d to pretty-printed JSON with a trailing
// newline. Deterministic: equal descriptors marshal byte-identically.
func Marshal(d *Descriptor) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Unmarshal parses and validates descriptor bytes.
func Unmarshal(data []byte) (*Descriptor, error) {
	var d Descriptor
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("descriptor: parse: %w", err)
	}
	if dec.More() {
		return nil, errors.New("descriptor: trailing content after JSON object")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// Read pulls the L0 descriptor through a Driver. Returns
// os.ErrNotExist (via the driver) when none is present.
func Read(ctx context.Context, drv driver.Driver) (*Descriptor, error) {
	rc, err := drv.Get(ctx, Path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("descriptor: read: %w", err)
	}
	return Unmarshal(data)
}

// WriteBoth writes d to both replicas (L0 and L1), serialised once and
// Put twice. This is the canonical descriptor write; every mutation
// path (InitStore, Unlock, SetPassphrase, RotateKEK) goes through it.
//
// Each Put is atomic; the pair is not. A crash between the two leaves
// L1 stale, which reconcile heals on the next OpenStore.
func WriteBoth(ctx context.Context, drv driver.Driver, d *Descriptor) error {
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	if err := drv.Put(ctx, Path, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("descriptor.WriteBoth: L0: %w", err)
	}
	if err := drv.Put(ctx, BackupPath, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("descriptor.WriteBoth: L1: %w", err)
	}
	return nil
}

// WriteReplica writes d to one specific replica. Used by reconcile
// self-heal (to overwrite the damaged side without touching the good
// one) and by tests fabricating divergence. Driver.Put is atomic.
func WriteReplica(ctx context.Context, drv driver.Driver, d *Descriptor, r Replica) error {
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	path, err := r.Path()
	if err != nil {
		return fmt.Errorf("descriptor.WriteReplica: %w", err)
	}
	return drv.Put(ctx, path, bytes.NewReader(data))
}

// Replica identifies one of the two on-disk descriptor copies.
type Replica int

const (
	// L0 is the primary descriptor (store.json).
	L0 Replica = iota
	// L1 is the shadow descriptor (.store.backup.json).
	L1
)

// String returns the canonical short name ("L0" or "L1").
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

// Path returns the replica's path relative to the driver root, or an
// error for an out-of-range value (so a typo'd cast is loud).
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

package descriptor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/rkurbatov/scrinium/driver"
)

// CurrentSchemaVersion is the schema version this build writes.
// A binary reading a descriptor with schema_version > CurrentSchemaVersion
// must refuse to open the Store.
const CurrentSchemaVersion = 1

// Path is the descriptor file location relative to the driver root.
const Path = "store.json"

// Descriptor is the on-disk shape of store.json per §10.1.3.
//
// Holds Store identity and the cryptographic envelope. Projection
// parameters (PathTopology, ManifestStorage, ManifestEncoding,
// ManifestCrypto, ContentHasher, DeletionPolicyLock) live in the
// system.config artifact pointed to by system.config/current
// (§10.1.4) — not here.
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
	Algorithm string `json:"algorithm"` // "argon2id"
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

// Marshal serialises d to pretty-printed JSON with a trailing newline.
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

// Read pulls the descriptor through a Driver. Returns os.ErrNotExist
// (via the driver) when no descriptor is present.
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

// Write serialises and stores the descriptor through a Driver.
// Driver.Put is atomic (temp + rename). Writes only L0 (Path) —
// callers wanting the L0+L1 invariant must use WriteBoth.
func Write(ctx context.Context, drv driver.Driver, d *Descriptor) error {
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	return drv.Put(ctx, Path, bytes.NewReader(data))
}

// WriteBoth serialises d once and writes the result to both L0
// (Path) and L1 (BackupPath). Each Put is atomic; the pair is
// not — a crash between them leaves L1 stale and L0 fresh, which
// Reconcile heals on the next OpenStore.
//
// Per §10.1.5 the on-disk invariant after a successful WriteBoth
// is "L0 ⇄ L1, byte-identical". Reconcile is the recovery path
// for any other observed state.
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

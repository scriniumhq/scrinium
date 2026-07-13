package descriptor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/hashing"
	"scrinium.dev/engine/internal/named"
)

// canonicalHashes is the descriptor's fixed sha256 registry. The
// descriptor's hash algorithm is not configurable (unlike store content),
// so it carries its own registry rather than depending on the wired store
// registry, which pre-config and recovery paths do not have in scope.
var canonicalHashes = hashing.NewHashRegistry().
	Register(string(config.HashSHA256), func() hash.Hash { return sha256.New() })

// CanonicalHashes returns the descriptor's fixed sha256 hash registry, for
// callers that must write or read a descriptor cell without a wired store
// registry (crypto rekey, recovery-kit restore).
func CanonicalHashes() domain.HashRegistry { return canonicalHashes }

// CurrentSchemaVersion is the schema version this build writes. A
// descriptor with a higher schema_version must be refused.
const CurrentSchemaVersion = 1

// Name is the primary descriptor's system-artifact name (ADR-103): a
// keep=0 named cell, NOT a bare file. Read first at bootstrap via the
// named layer (bare driver + hash registry, Plain), envelope-EXEMPT —
// the descriptor carries the store identity the envelope would check,
// so it cannot itself be wrapped in an identity envelope.
const Name = "store.descriptor"

// BackupName is the shadow replica's cell name, written synchronously
// with Name on every mutation and used by reconcile to self-heal.
const BackupName = "store.descriptor.backup"

// Descriptor is the on-disk shape of the descriptor cell: Store identity
// and the crypto material. Projection parameters live in system.config,
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

// writeReplicaCell serialises an already-validated descriptor into a
// Plain inline manifest and writes it to one replica cell, last-write-wins
// (exclusive=false; the descriptor is a keep=0 cell, not a lock). The
// manifest is named after the cell, hashed with the canonical sha256, and
// carries no crypto (Plain) — it is read pre-DEK at bootstrap.
func writeReplicaCell(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, name string, data []byte) error {
	body, _, err := named.BuildInlineManifest(name, data, string(config.HashSHA256), hashes, config.ManifestCryptoPlain, nil, "")
	if err != nil {
		return fmt.Errorf("descriptor: build manifest %q: %w", name, err)
	}
	return named.WriteCell(ctx, drv, name, body, false)
}

// Read pulls the primary descriptor (Name) through the named layer,
// verifying its content hash on read. Returns errs.ErrArtifactNotFound
// (via named.LoadCell) when no descriptor cell is present.
func Read(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry) (*Descriptor, error) {
	m, err := named.LoadCell(ctx, drv, hashes, Name)
	if err != nil {
		return nil, err
	}
	return Unmarshal(m.InlineBlob)
}

// WriteBoth writes d to both replica cells (Name and BackupName),
// serialised once and written twice. This is the canonical descriptor
// write; every mutation path (InitStore, Unlock, SetPassphrase,
// RotateKEK) goes through it.
//
// Each cell write is atomic; the pair is not. A crash between the two
// leaves the backup stale, which reconcile heals on the next Openconfig.
func WriteBoth(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, d *Descriptor) error {
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	if err := writeReplicaCell(ctx, drv, hashes, Name, data); err != nil {
		return fmt.Errorf("descriptor.WriteBoth: L0: %w", err)
	}
	if err := writeReplicaCell(ctx, drv, hashes, BackupName, data); err != nil {
		return fmt.Errorf("descriptor.WriteBoth: L1: %w", err)
	}
	return nil
}

// WriteReplica writes d to one specific replica cell. Used by reconcile
// self-heal (to overwrite the damaged side without touching the good
// one) and by tests fabricating divergence.
func WriteReplica(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, d *Descriptor, r Replica) error {
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	name, err := r.Name()
	if err != nil {
		return fmt.Errorf("descriptor.WriteReplica: %w", err)
	}
	return writeReplicaCell(ctx, drv, hashes, name, data)
}

// RemoveBoth deletes both replica cells. Idempotent (an absent cell is
// not an error). Used by force-reinit to clear a prior store identity.
func RemoveBoth(ctx context.Context, drv driver.Driver) error {
	if err := named.RemoveCell(ctx, drv, Name); err != nil {
		return fmt.Errorf("descriptor.RemoveBoth: %s: %w", Name, err)
	}
	if err := named.RemoveCell(ctx, drv, BackupName); err != nil {
		return fmt.Errorf("descriptor.RemoveBoth: %s: %w", BackupName, err)
	}
	return nil
}

// Replica identifies one of the two descriptor cell copies.
type Replica int

const (
	// L0 is the primary descriptor (Name).
	L0 Replica = iota
	// L1 is the shadow descriptor (BackupName).
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

// Name returns the replica's cell name, or an error for an out-of-range
// value (so a typo'd cast is loud).
func (r Replica) Name() (string, error) {
	switch r {
	case L0:
		return Name, nil
	case L1:
		return BackupName, nil
	default:
		return "", fmt.Errorf("descriptor: unknown Replica %d", int(r))
	}
}

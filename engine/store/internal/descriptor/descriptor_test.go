package descriptor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/engine/internal/named"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
)

var testHashes = CanonicalHashes()

// validDescriptor returns a descriptor with one of every field set.
func validDescriptor(t *testing.T) *Descriptor {
	t.Helper()
	return &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      7,
	}
}

func TestValidate_OK(t *testing.T) {
	if err := validDescriptor(t).Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidate_RejectsEmptyStoreID(t *testing.T) {
	d := validDescriptor(t)
	d.StoreID = ""
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on empty StoreID")
	}
}

func TestValidate_RejectsZeroSchemaVersion(t *testing.T) {
	d := validDescriptor(t)
	d.SchemaVersion = 0
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on zero SchemaVersion")
	}
}

func TestValidate_RejectsFutureSchemaVersion(t *testing.T) {
	d := validDescriptor(t)
	d.SchemaVersion = CurrentSchemaVersion + 1
	err := d.Validate()
	if err == nil {
		t.Fatal("expected error on future SchemaVersion")
	}
	if !strings.Contains(err.Error(), "exceeds supported") {
		t.Errorf("error should mention version mismatch: %v", err)
	}
}

func TestValidate_RejectsZeroSequence(t *testing.T) {
	d := validDescriptor(t)
	d.Sequence = 0
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on zero Sequence")
	}
}

func TestValidate_DEKEncryptedWithoutDEK(t *testing.T) {
	d := validDescriptor(t)
	d.DEKEncrypted = true
	d.DEK = nil
	d.KDFParams = &KDFParams{Algorithm: "argon2id", Time: 1, Memory: 19456, Threads: 1, Salt: []byte{1}}
	if err := d.Validate(); err == nil {
		t.Fatal("expected error: DEKEncrypted=true with empty DEK")
	}
}

func TestValidate_DEKEncryptedWithoutKDFParams(t *testing.T) {
	d := validDescriptor(t)
	d.DEKEncrypted = true
	d.DEK = []byte{1, 2, 3}
	d.KDFParams = nil
	if err := d.Validate(); err == nil {
		t.Fatal("expected error: DEKEncrypted=true without KDFParams")
	}
}

func TestValidate_PlainStoreOK(t *testing.T) {
	d := validDescriptor(t)
	d.DEKEncrypted = false
	d.DEK = nil
	d.KDFParams = nil
	if err := d.Validate(); err != nil {
		t.Fatalf("Plain store should be valid, got %v", err)
	}
}

func TestMarshalUnmarshal_RoundTrip(t *testing.T) {
	src := validDescriptor(t)
	data, err := Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if data[len(data)-1] != '\n' {
		t.Error("Marshal output should end with newline")
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID: got %q, want %q", got.StoreID, src.StoreID)
	}
	if got.SchemaVersion != src.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, src.SchemaVersion)
	}
	if got.Sequence != src.Sequence {
		t.Errorf("Sequence: got %d, want %d", got.Sequence, src.Sequence)
	}
}

func TestUnmarshal_RejectsUnknownField(t *testing.T) {
	bad := []byte(`{"store_id":"11111111-2222-3333-4444-555555555555","schema_version":1,"sequence":1,"unknown_extra_field":"value"}`)
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("expected error on unknown field")
	}
}

// Legacy descriptors carried projection fields (PathTopology, …) that
// now live in system.config; Unmarshal must reject them.
func TestUnmarshal_RejectsLegacyProjectionFields(t *testing.T) {
	bad := []byte(`{"store_id":"11111111-2222-3333-4444-555555555555","schema_version":1,"sequence":1,"pathTopology":"Sharded"}`)
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("expected legacy pathTopology field to be rejected")
	}
}

func TestUnmarshal_RejectsTrailingContent(t *testing.T) {
	d, _ := Marshal(validDescriptor(t))
	bad := append(d, []byte(`{"another":"document"}`)...)
	_, err := Unmarshal(bad)
	if err == nil {
		t.Fatal("expected error on trailing content")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error should mention trailing content: %v", err)
	}
}

func TestUnmarshal_RejectsMalformedJSON(t *testing.T) {
	if _, err := Unmarshal([]byte(`{not json`)); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestWriteBoth_Read_RoundTrip(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor(t)
	if err := WriteBoth(context.Background(), drv, testHashes, src); err != nil {
		t.Fatalf("WriteBoth: %v", err)
	}
	got, err := Read(context.Background(), drv, testHashes)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID round-trip: got %q, want %q", got.StoreID, src.StoreID)
	}
	if got.Sequence != src.Sequence {
		t.Errorf("Sequence round-trip: got %d, want %d", got.Sequence, src.Sequence)
	}
}

func TestWriteReplica_RoundTripL0(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor(t)
	if err := WriteReplica(context.Background(), drv, testHashes, src, L0); err != nil {
		t.Fatalf("WriteReplica(L0): %v", err)
	}
	got, err := Read(context.Background(), drv, testHashes)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID round-trip: got %q, want %q", got.StoreID, src.StoreID)
	}
}

// L1 is verified by reading the backup path directly: Read targets L0,
// and the replica-status read lives in the reconcile subpackage.
func TestWriteReplica_RoundTripL1(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor(t)
	if err := WriteReplica(context.Background(), drv, testHashes, src, L1); err != nil {
		t.Fatalf("WriteReplica(L1): %v", err)
	}
	m, err := named.LoadCell(context.Background(), drv, testHashes, BackupName)
	if err != nil {
		t.Fatalf("load L1 cell: %v", err)
	}
	got, err := Unmarshal(m.InlineBlob)
	if err != nil {
		t.Fatalf("unmarshal L1: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID round-trip: got %q, want %q", got.StoreID, src.StoreID)
	}
}

func TestWriteReplica_RejectsInvalidReplica(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor(t)
	if err := WriteReplica(context.Background(), drv, testHashes, src, Replica(99)); err == nil {
		t.Fatal("expected error on invalid Replica value")
	}
}

func TestRead_NotFound(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, err := Read(context.Background(), drv, testHashes)
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected ErrArtifactNotFound, got %v", err)
	}
}

package descriptor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"scrinium.dev/internal/testutil/driverfx"
)

func validDescriptor() *Descriptor {
	return &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      1,
		DEK:           nil,
		DEKEncrypted:  false,
	}
}

// --- Validate ---

func TestValidate_OK(t *testing.T) {
	if err := validDescriptor().Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidate_RejectsEmptyStoreID(t *testing.T) {
	d := validDescriptor()
	d.StoreID = ""
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on empty StoreID")
	}
}

func TestValidate_RejectsZeroSchemaVersion(t *testing.T) {
	d := validDescriptor()
	d.SchemaVersion = 0
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on zero SchemaVersion")
	}
}

func TestValidate_RejectsFutureSchemaVersion(t *testing.T) {
	d := validDescriptor()
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
	d := validDescriptor()
	d.Sequence = 0
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on zero Sequence")
	}
}

func TestValidate_DEKEncryptedWithoutDEK(t *testing.T) {
	d := validDescriptor()
	d.DEKEncrypted = true
	d.DEK = nil
	d.KDFParams = &KDFParams{Algorithm: "argon2id", Time: 1, Memory: 19456, Threads: 1, Salt: []byte{1}}
	if err := d.Validate(); err == nil {
		t.Fatal("expected error: DEKEncrypted=true with empty DEK")
	}
}

func TestValidate_DEKEncryptedWithoutKDFParams(t *testing.T) {
	d := validDescriptor()
	d.DEKEncrypted = true
	d.DEK = []byte{1, 2, 3}
	d.KDFParams = nil
	if err := d.Validate(); err == nil {
		t.Fatal("expected error: DEKEncrypted=true without KDFParams")
	}
}

func TestValidate_PlainStoreOK(t *testing.T) {
	d := validDescriptor()
	d.DEKEncrypted = false
	d.DEK = nil
	d.KDFParams = nil
	if err := d.Validate(); err != nil {
		t.Fatalf("Plain store should be valid, got %v", err)
	}
}

// --- Marshal / Unmarshal ---

func TestMarshalUnmarshal_RoundTrip(t *testing.T) {
	src := validDescriptor()
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
	bad := []byte(`{
		"store_id": "11111111-2222-3333-4444-555555555555",
		"schema_version": 1,
		"sequence": 1,
		"dek": null,
		"dek_encrypted": false,
		"unknown_extra_field": "value"
	}`)
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func TestUnmarshal_RejectsLegacyProjectionFields(t *testing.T) {
	// Pre-§10.1.3 descriptors carried PathTopology, ManifestStorage, etc.
	// New code rejects them: they are now in system.config.
	bad := []byte(`{
		"store_id": "11111111-2222-3333-4444-555555555555",
		"schema_version": 1,
		"sequence": 1,
		"dek": null,
		"dek_encrypted": false,
		"pathTopology": "Sharded"
	}`)
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("expected legacy pathTopology field to be rejected")
	}
}

func TestUnmarshal_RejectsTrailingContent(t *testing.T) {
	d, _ := Marshal(validDescriptor())
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

// --- Read / Write through localfs ---

func TestPersist_Read_RoundTrip(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor()

	if err := Persist(context.Background(), drv, src); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	got, err := Read(context.Background(), drv)
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

// TestWriteReplica_RoundTripL0 covers the low-level repair API
// against the L0 path. Persist's L0 leg goes through the same
// code path; this test exists separately so a regression in
// WriteReplica fails fast even if Persist still works.
func TestWriteReplica_RoundTripL0(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor()

	if err := WriteReplica(context.Background(), drv, src, L0); err != nil {
		t.Fatalf("WriteReplica(L0): %v", err)
	}
	got, err := Read(context.Background(), drv)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID round-trip: got %q, want %q", got.StoreID, src.StoreID)
	}
}

// TestWriteReplica_RoundTripL1 covers the L1 leg of the repair
// API. Reads the L1 file directly through ReadReplica because
// Read targets L0 only.
func TestWriteReplica_RoundTripL1(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor()

	if err := WriteReplica(context.Background(), drv, src, L1); err != nil {
		t.Fatalf("WriteReplica(L1): %v", err)
	}
	got, status, err := ReadReplica(context.Background(), drv, BackupPath)
	if err != nil {
		t.Fatalf("ReadReplica: %v", err)
	}
	if status != ReplicaValid {
		t.Fatalf("L1 status: got %v, want ReplicaValid", status)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID round-trip: got %q, want %q", got.StoreID, src.StoreID)
	}
}

func TestWriteReplica_RejectsInvalidReplica(t *testing.T) {
	drv := driverfx.LocalFS(t)
	src := validDescriptor()
	if err := WriteReplica(context.Background(), drv, src, Replica(99)); err == nil {
		t.Fatal("expected error on invalid Replica value")
	}
}

func TestRead_NotFound(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, err := Read(context.Background(), drv)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

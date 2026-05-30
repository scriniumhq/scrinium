package descriptor

import (
	"bytes"
	"testing"
)

func TestChecksum_DeterministicForEqualDescriptors(t *testing.T) {
	a := &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      1,
	}
	b := &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      1,
	}
	cs1, err := Checksum(a)
	if err != nil {
		t.Fatal(err)
	}
	cs2, err := Checksum(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cs1, cs2) {
		t.Fatal("Checksum not deterministic for Equal descriptors")
	}
	if len(cs1) != ChecksumLen {
		t.Errorf("Checksum length: got %d, want %d", len(cs1), ChecksumLen)
	}
}

func TestChecksum_DiffersOnAnyFieldChange(t *testing.T) {
	base := &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      1,
	}
	cs0, _ := Checksum(base)

	for _, mutate := range []struct {
		name string
		f    func(d *Descriptor)
	}{
		{"StoreID", func(d *Descriptor) { d.StoreID = "different" }},
		{"Sequence", func(d *Descriptor) { d.Sequence = 999 }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			cp := *base
			mutate.f(&cp)
			cs, err := Checksum(&cp)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(cs0, cs) {
				t.Errorf("Checksum unchanged after mutating %s", mutate.name)
			}
		})
	}
}

func TestChecksum_NilDescriptor(t *testing.T) {
	if _, err := Checksum(nil); err == nil {
		t.Error("Checksum(nil) should error")
	}
}

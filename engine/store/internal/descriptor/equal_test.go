package descriptor

import "testing"

func TestEqual_NilCases(t *testing.T) {
	if !Equal(nil, nil) {
		t.Error("Equal(nil, nil) should be true")
	}
	good := &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: CurrentSchemaVersion,
		Sequence:      1,
	}
	if Equal(good, nil) {
		t.Error("Equal(good, nil) should be false")
	}
	if Equal(nil, good) {
		t.Error("Equal(nil, good) should be false")
	}
}

func TestEqual_FieldByField(t *testing.T) {
	base := &Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: 1,
		Sequence:      5,
		DEK:           []byte{1, 2, 3},
		DEKEncrypted:  true,
		KDFParams: &KDFParams{
			Algorithm: "argon2id",
			Time:      1, Memory: 65536, Threads: 4,
			Salt: []byte{0xAA, 0xBB},
		},
	}

	cp := *base
	cpKDF := *base.KDFParams
	cpKDF.Salt = []byte{0xAA, 0xBB}
	cp.KDFParams = &cpKDF
	cp.DEK = []byte{1, 2, 3}

	if !Equal(base, &cp) {
		t.Fatal("identical content should be Equal")
	}

	// Each mutation should break equality.
	for _, mutate := range []struct {
		name string
		f    func(d *Descriptor)
	}{
		{"StoreID", func(d *Descriptor) { d.StoreID = "x" }},
		{"SchemaVersion", func(d *Descriptor) { d.SchemaVersion = 99 }},
		{"Sequence", func(d *Descriptor) { d.Sequence = 999 }},
		{"DEKEncrypted", func(d *Descriptor) { d.DEKEncrypted = false }},
		{"DEK", func(d *Descriptor) { d.DEK = []byte{9} }},
		{"KDF.Time", func(d *Descriptor) { d.KDFParams.Time = 99 }},
		{"KDF.Memory", func(d *Descriptor) { d.KDFParams.Memory = 99 }},
		{"KDF.Threads", func(d *Descriptor) { d.KDFParams.Threads = 99 }},
		{"KDF.Salt", func(d *Descriptor) { d.KDFParams.Salt = []byte{0xFF} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			a := *base
			aKDF := *base.KDFParams
			aKDF.Salt = append([]byte{}, base.KDFParams.Salt...)
			a.KDFParams = &aKDF
			a.DEK = append([]byte{}, base.DEK...)
			mutate.f(&a)
			if Equal(base, &a) {
				t.Errorf("Equal returned true after mutating %s", mutate.name)
			}
		})
	}
}

func TestEqual_KDFParamsNilOnOneSide(t *testing.T) {
	a := &Descriptor{StoreID: "x", SchemaVersion: 1, Sequence: 1}
	b := &Descriptor{StoreID: "x", SchemaVersion: 1, Sequence: 1,
		KDFParams: &KDFParams{Algorithm: "argon2id", Time: 1, Memory: 65536, Threads: 4, Salt: []byte{0x00}}}

	if Equal(a, b) {
		t.Error("KDFParams nil on one side: should not be Equal")
	}
	if Equal(b, a) {
		t.Error("KDFParams nil on the other side: should not be Equal")
	}
}

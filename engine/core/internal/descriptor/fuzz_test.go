package descriptor_test

import (
	"testing"

	"github.com/rkurbatov/scrinium/engine/core/internal/descriptor"
)

// FuzzUnmarshal probes descriptor.Unmarshal against arbitrary
// byte input. Contract: never panic, never produce a Descriptor
// that fails its own Validate() check.
//
// The seeds cover the JSON shapes we care about: a real valid
// descriptor (the happy path), the tiniest valid-by-Validate
// shape, and several near-misses that exercise specific Validate
// branches. The fuzzer mutates outward from these.
func FuzzUnmarshal(f *testing.F) {
	// Seed: a real, validated descriptor produced by Marshal.
	valid := &descriptor.Descriptor{
		StoreID:       "01HZQF8K6T5N3M2P9V4XW7Y8B6",
		SchemaVersion: 1,
		Sequence:      1,
	}
	validBytes, err := descriptor.Marshal(valid)
	if err != nil {
		f.Fatalf("seed marshal: %v", err)
	}
	f.Add(validBytes)

	// Seed: minimal valid JSON that satisfies Validate.
	f.Add([]byte(`{"store_id":"x","schema_version":1,"sequence":1}`))

	// Seed: the same but with a future schema version — exercises
	// the "schema_version exceeds supported" branch.
	f.Add([]byte(`{"store_id":"x","schema_version":99,"sequence":1}`))

	// Seed: dek_encrypted true but empty dek/kdf — exercises the
	// crypto-envelope validation.
	f.Add([]byte(`{"store_id":"x","schema_version":1,"sequence":1,"dek_encrypted":true}`))

	// Seed: dek_encrypted with kdf_params — exercises the nested
	// struct path.
	f.Add([]byte(`{"store_id":"x","schema_version":1,"sequence":1,"dek_encrypted":true,"dek":"AAAA","kdf_params":{"algorithm":"argon2id","time":1,"memory":65536,"threads":1,"salt":"AAAA"}}`))

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: just whitespace.
	f.Add([]byte("   \n\t  "))

	// Seed: malformed JSON, but starts like the real shape.
	f.Add([]byte(`{"store_id":"x"`))

	// Seed: trailing garbage after a valid object — exercises the
	// "trailing content" check.
	f.Add([]byte(`{"store_id":"x","schema_version":1,"sequence":1}garbage`))

	// Seed: unknown field — exercises DisallowUnknownFields.
	f.Add([]byte(`{"store_id":"x","schema_version":1,"sequence":1,"unexpected":"field"}`))

	// Seed: a JSON array instead of an object — type mismatch at
	// the top level.
	f.Add([]byte(`[]`))

	// Seed: nested object that would tax json.Decoder if anything
	// is too clever about reflection.
	f.Add([]byte(`{"store_id":"a","schema_version":1,"sequence":1,"kdf_params":null}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Property 1: never panic.
		d, err := descriptor.Unmarshal(data)
		if err != nil {
			// Most inputs will reach here. Validate that on
			// error we always get a nil pointer — never a
			// half-built struct that callers might mistakenly
			// use.
			if d != nil {
				t.Errorf("Unmarshal returned non-nil descriptor with error: input=%q d=%+v err=%v",
					data, d, err)
			}
			return
		}

		// Property 2: a successfully-unmarshaled descriptor must
		// pass its own Validate. Unmarshal calls Validate
		// internally; this assertion guards against a regression
		// where the call is removed.
		if verr := d.Validate(); verr != nil {
			t.Errorf("Unmarshal returned a descriptor that fails Validate: input=%q d=%+v verr=%v",
				data, d, verr)
		}

		// Property 3: round-trip stability. Marshal must accept
		// what Unmarshal returned, and re-Unmarshal must yield
		// an equivalent value. The bytes themselves don't have
		// to match — Unmarshal accepts arbitrary key order while
		// Marshal canonicalises.
		reBytes, err := descriptor.Marshal(d)
		if err != nil {
			t.Fatalf("re-marshal failed for descriptor that decoded cleanly: d=%+v err=%v", d, err)
		}
		d2, err := descriptor.Unmarshal(reBytes)
		if err != nil {
			t.Fatalf("re-unmarshal failed: d=%+v reBytes=%s err=%v", d, reBytes, err)
		}
		if !descriptor.Equal(d, d2) {
			t.Errorf("round-trip changed the descriptor:\n  d1=%+v\n  d2=%+v", d, d2)
		}
	})
}

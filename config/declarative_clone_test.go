package config

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// fullPolicyFixture is a Policy with every reference-typed field populated,
// including nested maps/slices inside a pipeline stage's free-form params.
// It is the input the clone tests exercise; TestClonePolicy_FixtureCovers...
// fails if a new reference field is added to Policy without being set here.
func fullPolicyFixture() *Policy {
	return &Policy{
		Encryption: &Encryption{Passphrase: "env:SECRET", Mode: "sealed", Dedup: "convergent", SegmentSize: 4096},
		Chunking:   &Chunking{MaxSize: 1 << 20, DirectWriteThreshold: 1 << 19},
		Bundling:   &Bundling{},
		GC:         &Schedule{Every: Duration(time.Hour)},
		Scrub:      &ScrubSchedule{Every: Duration(2 * time.Hour)},
		Checkpoint: &Schedule{Schedule: "0 3 * * *"},
		Pipeline: []PipelineStage{
			{Kind: "compress", Params: map[string]any{
				"algo":   "zstd",
				"nested": map[string]any{"level": 3},
				"list":   []any{1, 2, map[string]any{"x": "y"}},
			}},
		},
		PipelineExtra: []PipelineStage{
			{Kind: "hash", Params: map[string]any{"fn": "sha256"}},
		},
		DeletionPolicy:  "retention",
		Retention:       Duration(24 * time.Hour),
		MaxArtifactSize: Size(1 << 30),
	}
}

func TestClonePolicy_Nil(t *testing.T) {
	if clonePolicy(nil) != nil {
		t.Error("clonePolicy(nil) must be nil")
	}
}

// The fixture must populate every reference-typed (pointer/slice/map) field
// of Policy. If this fails, a new such field was added: set it in
// fullPolicyFixture and make sure clonePolicy deep-copies it.
func TestClonePolicy_FixtureCoversAllRefFields(t *testing.T) {
	v := reflect.ValueOf(fullPolicyFixture()).Elem()
	tp := v.Type()
	for i := 0; i < v.NumField(); i++ {
		fv, name := v.Field(i), tp.Field(i).Name
		switch fv.Kind() {
		case reflect.Ptr, reflect.Map:
			if fv.IsNil() {
				t.Errorf("Policy.%s is a reference field but the fixture leaves it nil", name)
			}
		case reflect.Slice:
			if fv.IsNil() || fv.Len() == 0 {
				t.Errorf("Policy.%s is a slice but the fixture leaves it empty", name)
			}
		}
	}
}

// clonePolicy must produce a faithful but fully independent copy: equal by
// value, sharing no pointer, slice, or map with the original.
func TestClonePolicy_DeepIndependent(t *testing.T) {
	orig := fullPolicyFixture()
	clone := clonePolicy(orig)

	if !reflect.DeepEqual(orig, clone) {
		t.Fatal("clone is not value-equal to the original")
	}
	assertNoSharedRefs(t, reflect.ValueOf(orig), reflect.ValueOf(clone), "Policy")

	// Spot-check the map that the old append-based clone left aliased.
	clone.Pipeline[0].Params["algo"] = "MUTATED"
	if orig.Pipeline[0].Params["algo"] == "MUTATED" {
		t.Error("mutating clone pipeline params bled into the original (aliased map)")
	}
}

// assertNoSharedRefs walks two same-typed values and fails if any pointer,
// slice backing array, or map is shared between them. It recurses through
// structs, pointers, slices, maps, and interface values so a reference at
// any depth is checked.
func assertNoSharedRefs(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	if a.Type() != b.Type() {
		t.Fatalf("%s: type mismatch %v vs %v", path, a.Type(), b.Type())
	}
	switch a.Kind() {
	case reflect.Ptr:
		if a.IsNil() || b.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s: pointer shared between clone and original", path)
			return
		}
		assertNoSharedRefs(t, a.Elem(), b.Elem(), path)
	case reflect.Slice:
		if a.IsNil() || b.IsNil() {
			return
		}
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			t.Errorf("%s: slice backing array shared", path)
		}
		n := a.Len()
		if b.Len() < n {
			n = b.Len()
		}
		for i := 0; i < n; i++ {
			assertNoSharedRefs(t, a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.Map:
		if a.IsNil() || b.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s: map shared between clone and original", path)
			return
		}
		for _, k := range a.MapKeys() {
			av, bv := a.MapIndex(k), b.MapIndex(k)
			if bv.IsValid() {
				assertNoSharedRefs(t, av, bv, fmt.Sprintf("%s[%v]", path, k))
			}
		}
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return
		}
		assertNoSharedRefs(t, a.Elem(), b.Elem(), path)
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if a.Type().Field(i).PkgPath != "" {
				continue // unexported
			}
			assertNoSharedRefs(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
		}
	}
}

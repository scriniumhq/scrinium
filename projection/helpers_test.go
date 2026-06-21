// White-box tests for the Config→FSOps mapping helpers. They are in
// package projection (not projection_test) because the helpers are
// unexported; fsops is imported only to name the EditingPolicy values,
// which are pure-bool structs comparable with ==.
package projection

import (
	"testing"

	"scrinium.dev/projection/internal/fsops"
)

func TestEditingPolicy(t *testing.T) {
	tr, fa := true, false
	tests := []struct {
		name string
		cfg  Config
		want fsops.EditingPolicy
	}{
		{"empty is off", Config{}, fsops.EditingOff()},
		{"off is off", Config{Editing: "off"}, fsops.EditingOff()},
		{"on is all-enabled", Config{Editing: "on"}, fsops.EditingOn()},
		{"unknown falls back to off", Config{Editing: "weird"}, fsops.EditingOff()},
		{
			"custom honors the set flags",
			Config{Editing: "custom", AllowRename: &tr, AllowTruncate: &tr, AllowSetattr: &fa},
			fsops.EditingPolicy{AllowRename: true, AllowTruncate: true},
		},
		{
			"custom with nil flags is all-false",
			Config{Editing: "custom"},
			fsops.EditingPolicy{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := editingPolicy(tt.cfg); got != tt.want {
				t.Errorf("editingPolicy(%+v) = %+v, want %+v", tt.cfg, got, tt.want)
			}
		})
	}
}

func TestDefaultID(t *testing.T) {
	if got := defaultID(42, func() int { return 7 }); got != 42 {
		t.Errorf("defaultID(42, …) = %d, want 42 (a nonzero value wins)", got)
	}
	if got := defaultID(0, func() int { return 1000 }); got != 1000 {
		t.Errorf("defaultID(0, →1000) = %d, want 1000 (fallback used)", got)
	}
	if got := defaultID(0, func() int { return -1 }); got != 0 {
		t.Errorf("defaultID(0, →-1) = %d, want 0 (negative fallback clamped)", got)
	}
}

func TestDerefBool(t *testing.T) {
	tr, fa := true, false
	if derefBool(nil) {
		t.Error("derefBool(nil) = true, want false")
	}
	if derefBool(&fa) {
		t.Error("derefBool(&false) = true, want false")
	}
	if !derefBool(&tr) {
		t.Error("derefBool(&true) = false, want true")
	}
}

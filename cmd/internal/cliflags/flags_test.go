package cliflags

import (
	"strings"
	"testing"

	"scrinium.dev/projection/node"
)

func TestRootViewFlag_Set(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    node.RootView
		wantErr bool
	}{
		{"by-path", "by-path", node.RootByPath, false},
		{"by-session", "by-session", node.RootBySession, false},
		{"by-namespace", "by-namespace", node.RootByNamespace, false},
		{"by-date", "by-date", node.RootByDate, false},
		{"by-artifact", "by-artifact", node.RootByArtifact, false},
		{"invalid", "by-something", "", true},
		{"empty", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rv node.RootView
			f := RootViewFlag{P: &rv}
			err := f.Set(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Set(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && rv != tc.want {
				t.Errorf("Set(%q) = %q, want %q", tc.input, rv, tc.want)
			}
		})
	}
}

func TestRootViewFlag_String(t *testing.T) {
	var rv node.RootView = node.RootByDate
	f := RootViewFlag{P: &rv}
	if got := f.String(); got != "by-date" {
		t.Errorf("String() = %q, want %q", got, "by-date")
	}
	// nil pointer — empty string, no panic
	if got := (RootViewFlag{}).String(); got != "" {
		t.Errorf("nil String() = %q, want \"\"", got)
	}
}

func TestBoolPtrFlag_Set(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{"true", "true", true, false},
		{"false", "false", false, false},
		{"1", "1", true, false},
		{"0", "0", false, false},
		{"invalid", "yes", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p *bool
			f := BoolPtrFlag{P: &p}
			err := f.Set(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Set(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr {
				if p == nil {
					t.Fatalf("Set(%q): pointer still nil", tc.input)
				}
				if *p != tc.want {
					t.Errorf("Set(%q): got %v, want %v", tc.input, *p, tc.want)
				}
			}
		})
	}
}

func TestBoolPtrFlag_String(t *testing.T) {
	tr := true
	p := &tr
	f := BoolPtrFlag{P: &p}
	if got := f.String(); got != "true" {
		t.Errorf("String() = %q, want %q", got, "true")
	}
	// nil inner — empty
	var p2 *bool
	f2 := BoolPtrFlag{P: &p2}
	if got := f2.String(); got != "" {
		t.Errorf("nil-inner String() = %q, want \"\"", got)
	}
}

func TestBoolPtrFlag_IsBoolFlag(t *testing.T) {
	if !(BoolPtrFlag{}).IsBoolFlag() {
		t.Errorf("IsBoolFlag() = false, want true")
	}
}

func TestByteSizeFlag_Set(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"plain", "500", 500, false},
		{"K-upper", "500K", 500 * 1024, false},
		{"K-lower", "500k", 500 * 1024, false},
		{"M-upper", "8M", 8 * 1024 * 1024, false},
		{"G-upper", "2G", 2 * 1024 * 1024 * 1024, false},
		{"T-upper", "1T", 1 << 40, false},
		{"zero", "0", 0, false},
		{"empty", "", 0, true},
		{"invalid", "abc", 0, true},
		{"invalid-suffix-only", "M", 0, true},
		{"with-space", " 100 ", 100, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var n int64
			f := ByteSizeFlag{P: &n}
			err := f.Set(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Set(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && n != tc.want {
				t.Errorf("Set(%q) = %d, want %d", tc.input, n, tc.want)
			}
		})
	}
}

func TestOctalFlag_Set(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint32
		wantErr bool
	}{
		{"with-leading-zero", "0644", 0o644, false},
		{"without-leading-zero", "644", 0o644, false},
		{"0o-prefix", "0o644", 0o644, false},
		{"empty-after-strip", "0", 0, false},
		{"all-zero", "0o0", 0, false},
		{"invalid-digit", "0789", 0, true},
		{"non-numeric", "rwx", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var n uint32
			f := OctalFlag{P: &n}
			err := f.Set(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Set(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && n != tc.want {
				t.Errorf("Set(%q) = %#o, want %#o", tc.input, n, tc.want)
			}
		})
	}
}

func TestOctalFlag_String(t *testing.T) {
	var n uint32 = 0o755
	f := OctalFlag{P: &n}
	if got := f.String(); !strings.HasPrefix(got, "0") || got != "0755" {
		t.Errorf("String() = %q, want %q", got, "0755")
	}
}

func TestUintFlag_Set(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint32
		wantErr bool
	}{
		{"zero", "0", 0, false},
		{"normal", "1000", 1000, false},
		{"max-uint32", "4294967295", 4294967295, false},
		{"overflow", "4294967296", 0, true},
		{"negative", "-1", 0, true},
		{"non-numeric", "abc", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var n uint32
			f := UintFlag{P: &n}
			err := f.Set(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Set(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && n != tc.want {
				t.Errorf("Set(%q) = %d, want %d", tc.input, n, tc.want)
			}
		})
	}
}

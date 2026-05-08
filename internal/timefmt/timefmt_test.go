package timefmt

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"second-precision", time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC),
			"2026-05-08T12:34:56Z"},
		{"sub-second-truncated", time.Date(2026, 5, 8, 12, 34, 56, 123456789, time.UTC),
			"2026-05-08T12:34:56Z"},
		{"non-utc-converted",
			time.Date(2026, 5, 8, 14, 34, 56, 0, time.FixedZone("CEST", 2*3600)),
			"2026-05-08T12:34:56Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Format(tc.in); got != tc.want {
				t.Errorf("Format(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{"empty-is-zero", "", time.Time{}, false},
		{"strict", "2026-05-08T12:34:56Z",
			time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC), false},
		{"nano", "2026-05-08T12:34:56.789Z",
			time.Date(2026, 5, 8, 12, 34, 56, 789000000, time.UTC), false},
		{"garbage", "not-a-date", time.Time{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse(%q) err = %v, wantErr = %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && !got.Equal(tc.want) {
				t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	// Critical contract: Format + Parse must round-trip at second
	// precision. RebuildIndex relies on this.
	original := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	s := Format(original)
	parsed, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !parsed.Equal(original) {
		t.Errorf("round-trip: original %v, formatted %q, parsed %v", original, s, parsed)
	}
}

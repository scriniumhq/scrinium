package timefmt

import "time"

// Format returns the canonical on-disk representation of t.
// Zero Time encodes to the empty string; callers map that to
// SQL NULL or omit the JSON field as appropriate.
func Format(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Parse reads a timestamp written by Format. Empty string
// returns zero Time (matches the absent/NULL semantics of both
// SQL NullString and JSON omitempty fields). Accepts the Nano
// variant too for forward compatibility.
func Parse(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

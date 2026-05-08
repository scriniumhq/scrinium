package humanize

import "testing"

func TestBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1500, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024*1024 + 512*1024, "1.5 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
		// Negative renders absolute value
		{-500, "500 B"},
		{-1500, "1.5 KiB"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := Bytes(tc.in); got != tc.want {
				t.Errorf("Bytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBytesSigned(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1500, "1.5 KiB"},
		{-500, "-500 B"},
		{-1500, "-1.5 KiB"},
		{-1024 * 1024, "-1.0 MiB"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := BytesSigned(tc.in); got != tc.want {
				t.Errorf("BytesSigned(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

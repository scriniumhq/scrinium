package vfs

import (
	"errors"
	"io"
	"io/fs"
	"testing"
)

func TestCalcSeek(t *testing.T) {
	const size, cur = 100, 30
	cases := []struct {
		name    string
		offset  int64
		whence  int
		want    int64
		wantErr bool
	}{
		{"start", 10, io.SeekStart, 10, false},
		{"current", 5, io.SeekCurrent, cur + 5, false},
		{"current_back", -10, io.SeekCurrent, cur - 10, false},
		{"end", -1, io.SeekEnd, size - 1, false},
		{"end_zero", 0, io.SeekEnd, size, false},
		{"start_zero", 0, io.SeekStart, 0, false},

		// Underflow clamps to 0 and errors (cursor reset semantics).
		{"underflow_start", -1, io.SeekStart, 0, true},
		{"underflow_current", -(cur + 1), io.SeekCurrent, 0, true},
		{"underflow_end", -(size + 1), io.SeekEnd, 0, true},

		// Unknown whence returns the live cursor unchanged, with an error.
		{"bad_whence", 7, 99, cur, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := calcSeek(tc.offset, tc.whence, cur, size)
			if got != tc.want {
				t.Errorf("offset = %d, want %d", got, tc.want)
			}
			if tc.wantErr && !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("err = %v, want fs.ErrInvalid", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected err = %v", err)
			}
		})
	}
}

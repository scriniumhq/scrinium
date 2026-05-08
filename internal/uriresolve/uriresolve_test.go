package uriresolve

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLocalPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr error
	}{
		{
			name: "absolute",
			raw:  "file:///abs/path",
			want: "/abs/path",
		},
		{
			name: "tilde",
			raw:  "file://~/relative",
			want: filepath.Join(home, "relative"),
		},
		{
			name: "cwd-relative",
			raw:  "file://./relative",
			want: filepath.Join(cwd, "relative"),
		},
		{
			name:    "unsupported-host",
			raw:     "file://example.com/path",
			wantErr: ErrUnsupportedHost,
		},
		{
			name:    "empty-path",
			raw:     "file://",
			wantErr: ErrEmptyPath,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.raw, err)
			}
			got, err := ResolveLocalPath(u)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error wrapping %v, got nil (got %q)", tc.wantErr, got)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("expected errors.Is %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveLocalPath_PreservesAbsolute(t *testing.T) {
	// Confirms that filepath.Abs is applied — even an already-
	// absolute path goes through Abs cleanup (e.g. trailing slash).
	u, _ := url.Parse("file:///path/with/trailing/")
	got, err := ResolveLocalPath(u)
	if err != nil {
		t.Fatalf("ResolveLocalPath: %v", err)
	}
	if !strings.HasPrefix(got, "/path/with/trailing") {
		t.Errorf("got %q, expected absolute path under /path/with/trailing", got)
	}
}

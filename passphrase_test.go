package scrinium

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rkurbatov/scrinium/core"
)

// TestLoadPassphraseProvider_Empty: empty path is the
// "Plain Store" signal; loader returns nil provider and nil
// error.
func TestLoadPassphraseProvider_Empty(t *testing.T) {
	pp, err := loadPassphraseProvider("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pp != nil {
		t.Errorf("expected nil provider for empty path, got non-nil")
	}
}

// TestLoadPassphraseProvider_Reads: the provider reads the
// file contents on each invocation. Trailing newlines are
// stripped.
func TestLoadPassphraseProvider_Reads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pass.txt")
	if err := os.WriteFile(path, []byte("super-secret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	pp, err := loadPassphraseProvider(path)
	if err != nil {
		t.Fatalf("loadPassphraseProvider: %v", err)
	}
	if pp == nil {
		t.Fatal("expected non-nil provider")
	}

	got, err := pp(context.Background(), core.PassphraseHint{})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	want := []byte("super-secret")
	if string(got) != string(want) {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestLoadPassphraseProvider_TrimmingTable: trailing CR/LF
// permutations are stripped; internal whitespace is
// preserved.
func TestLoadPassphraseProvider_TrimmingTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no_newline", "hunter2", "hunter2"},
		{"lf", "hunter2\n", "hunter2"},
		{"crlf", "hunter2\r\n", "hunter2"},
		{"cr", "hunter2\r", "hunter2"},
		{"internal_space", "hunter 2 with spaces\n", "hunter 2 with spaces"},
		{"internal_tab", "hunter\t2\n", "hunter\t2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pass.txt")
			if err := os.WriteFile(path, []byte(tc.in), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			pp, err := loadPassphraseProvider(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got, err := pp(context.Background(), core.PassphraseHint{})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestLoadPassphraseProvider_NotFound: missing file produces
// an error from the loader (not from a later call).
func TestLoadPassphraseProvider_NotFound(t *testing.T) {
	_, err := loadPassphraseProvider("/nonexistent/path/passphrase.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestLoadPassphraseProvider_Directory: pointing at a
// directory is a config error, surfaced clearly.
func TestLoadPassphraseProvider_Directory(t *testing.T) {
	dir := t.TempDir()
	_, err := loadPassphraseProvider(dir)
	if err == nil {
		t.Fatal("expected error for directory path")
	}
}

// TestLoadPassphraseProvider_EmptyAfterTrim: a file whose
// only content is a newline collapses to empty after
// trimming and is rejected.
func TestLoadPassphraseProvider_EmptyAfterTrim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pass.txt")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	pp, err := loadPassphraseProvider(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = pp(context.Background(), core.PassphraseHint{})
	if err == nil {
		t.Errorf("expected error from provider on empty-after-trim file")
	}
}

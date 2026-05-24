package secretref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBuiltins(t *testing.T) {
	ctx := context.Background()

	// plain:
	got, err := Ref("plain:hunter2").Resolve(ctx)
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("plain: got %q err %v", got, err)
	}

	// env:
	t.Setenv("SECRETREF_TEST_VAR", "from-env")
	got, err = Ref("env:SECRETREF_TEST_VAR").Resolve(ctx)
	if err != nil || string(got) != "from-env" {
		t.Fatalf("env: got %q err %v", got, err)
	}

	// file: trims trailing whitespace, keeps internal.
	dir := t.TempDir()
	fp := filepath.Join(dir, "pass")
	if err := os.WriteFile(fp, []byte("a b c\n\n  \t"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = Ref("file:" + fp).Resolve(ctx)
	if err != nil || string(got) != "a b c" {
		t.Fatalf("file: got %q err %v", got, err)
	}
}

func TestResolveErrors(t *testing.T) {
	ctx := context.Background()
	cases := []Ref{
		"",                       // empty
		"noscheme",               // no colon
		"bogus:whatever",         // unknown scheme
		"env:DEFINITELY_UNSET_X", // unset env
		"file:/no/such/file/xyz", // missing file
	}
	for _, c := range cases {
		if _, err := c.Resolve(ctx); err == nil {
			t.Errorf("Resolve(%q): expected error, got nil", c)
		}
	}
}

func TestRegisterAndResolve(t *testing.T) {
	Register("secretref-test-scheme", func(_ context.Context, v string) ([]byte, error) {
		return []byte("R:" + v), nil
	})
	got, err := Ref("secretref-test-scheme:x").Resolve(context.Background())
	if err != nil || string(got) != "R:x" {
		t.Fatalf("custom scheme: got %q err %v", got, err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate scheme registration")
		}
	}()
	Register("file", func(context.Context, string) ([]byte, error) { return nil, nil })
}

func TestRegisterEmptyOrNilPanics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scheme string
		r      Resolver
	}{
		{"empty", "", func(context.Context, string) ([]byte, error) { return nil, nil }},
		{"nil", "secretref-test-nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			Register(tc.scheme, tc.r)
		})
	}
}

func TestMaskingNeverLeaks(t *testing.T) {
	r := Ref("plain:topsecret")
	if strings.Contains(r.String(), "topsecret") {
		t.Errorf("String() leaked secret: %q", r.String())
	}
	if r.String() != "plain:<redacted>" {
		t.Errorf("String() = %q, want plain:<redacted>", r.String())
	}
	y, _ := r.MarshalYAML()
	if s, _ := y.(string); strings.Contains(s, "topsecret") {
		t.Errorf("MarshalYAML leaked: %q", s)
	}
	j, _ := r.MarshalJSON()
	if strings.Contains(string(j), "topsecret") {
		t.Errorf("MarshalJSON leaked: %q", j)
	}
	if Ref("").String() != "" {
		t.Errorf("empty ref String() should be empty")
	}
}

func TestIsZero(t *testing.T) {
	if !Ref("").IsZero() {
		t.Error("empty Ref should be zero")
	}
	if Ref("plain:x").IsZero() {
		t.Error("non-empty Ref should not be zero")
	}
}

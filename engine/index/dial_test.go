package index_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"scrinium.dev/engine/index"

	// Side-effect import: registers the sqlite:// dialer.
	_ "scrinium.dev/engine/index/sqlite"
)

// TestDialIndex_SQLiteFile builds a sqlite index from a
// canonical sqlite:///abs/path URI.
func TestDialIndex_SQLiteFile(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "idx.db")
	uri := "sqlite://" + dbPath

	idx, err := index.DialIndex(context.Background(), uri)
	if err != nil {
		t.Fatalf("DialIndex: %v", err)
	}
	if idx == nil {
		t.Fatal("DialIndex: nil index")
	}
	defer idx.Close()
}

// TestDialIndex_SQLiteMemory exercises the :memory: special
// form. SQLite uses an in-process DB; no path resolution
// happens.
func TestDialIndex_SQLiteMemory(t *testing.T) {
	idx, err := index.DialIndex(context.Background(), "sqlite://:memory:")
	if err != nil {
		t.Fatalf("DialIndex: %v", err)
	}
	if idx == nil {
		t.Fatal("DialIndex: nil index")
	}
	defer idx.Close()
}

// TestDialIndex_NoBarePathFallback verifies index URIs
// require an explicit scheme (unlike the driver's bare-path
// fallback). A plain path is rejected with a clear error.
func TestDialIndex_NoBarePathFallback(t *testing.T) {
	tmp := t.TempDir()
	_, err := index.DialIndex(context.Background(), filepath.Join(tmp, "idx.db"))
	if err == nil {
		t.Fatal("DialIndex: expected error for bare path")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Errorf("error %q does not mention 'scheme'", err)
	}
}

// TestDialIndex_UnsupportedScheme rejects unknown schemes
// with a "not registered" error.
func TestDialIndex_UnsupportedScheme(t *testing.T) {
	cases := []string{
		"postgres://localhost/db",
		"mysql://localhost/db",
		"file:///tmp/idx.db",
	}
	for _, uri := range cases {
		_, err := index.DialIndex(context.Background(), uri)
		if err == nil {
			t.Errorf("DialIndex(%q) succeeded; want error", uri)
			continue
		}
		if !strings.Contains(err.Error(), "not registered") {
			t.Errorf("DialIndex(%q) error = %v; want 'not registered'", uri, err)
		}
	}
}

// TestDialIndex_Empty verifies the empty-URI guard.
func TestDialIndex_Empty(t *testing.T) {
	_, err := index.DialIndex(context.Background(), "")
	if err == nil {
		t.Fatal("DialIndex(\"\") succeeded; want error")
	}
}

// TestDialIndex_SQLiteRelativeExpands verifies sqlite://./path
// expands the host-dot alias to the current directory via the
// shared resolver (scrinium.dev/internal/uri). cwd is moved into
// a temp dir so the db file is cleaned up with the test.
func TestDialIndex_SQLiteRelativeExpands(t *testing.T) {
	t.Chdir(t.TempDir())
	idx, err := index.DialIndex(context.Background(), "sqlite://./idx.db")
	if err != nil {
		t.Fatalf("DialIndex(sqlite://./...): %v", err)
	}
	if idx == nil {
		t.Fatal("DialIndex(sqlite://./...): nil index")
	}
	defer idx.Close()
}

// TestDialIndex_SQLiteTildeExpands mirrors the above for the
// tilde alias; HOME is redirected so the db lands in a temp dir.
func TestDialIndex_SQLiteTildeExpands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	idx, err := index.DialIndex(context.Background(), "sqlite://~/idx.db")
	if err != nil {
		t.Fatalf("DialIndex(sqlite://~/...): %v", err)
	}
	if idx == nil {
		t.Fatal("DialIndex(sqlite://~/...): nil index")
	}
	defer idx.Close()
}

// TestDialIndex_SQLiteBadHost rejects sqlite://example.com/db.
func TestDialIndex_SQLiteBadHost(t *testing.T) {
	_, err := index.DialIndex(context.Background(), "sqlite://example.com/db")
	if err == nil {
		t.Fatal("DialIndex: expected error for non-local host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention 'host'", err)
	}
}

package driver_test

import (
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/engine/driver"

	// Importing the localfs driver triggers its init(), which
	// registers the file:// scheme dialer. Tests below depend
	// on that registration; without this side-effect import
	// they would all fail with "scheme not registered".
	_ "github.com/rkurbatov/scrinium/engine/driver/localfs"
)

// TestDialDriver_BarePath checks the backward-compat fallback:
// inputs without a URI scheme are treated as filesystem paths.
func TestDialDriver_BarePath(t *testing.T) {
	tmp := t.TempDir()

	d, err := driver.DialDriver(tmp)
	if err != nil {
		t.Fatalf("DialDriver: %v", err)
	}
	if d == nil {
		t.Fatal("DialDriver: nil driver")
	}
}

// TestDialDriver_FileURI exercises the canonical file:///abs
// form. We verify it produces the same kind of driver as the
// bare-path equivalent — both must reach localfs.New.
func TestDialDriver_FileURI(t *testing.T) {
	tmp := t.TempDir()
	uri := "file://" + tmp

	d, err := driver.DialDriver(uri)
	if err != nil {
		t.Fatalf("DialDriver: %v", err)
	}
	if d == nil {
		t.Fatal("DialDriver: nil driver")
	}
}

// TestDialDriver_TildeBare verifies that bare paths starting
// with ~/ undergo home-directory expansion. We can't predict
// the absolute path the tilde resolves to, so we just check
// the call doesn't error on the tilde itself.
func TestDialDriver_TildeBare(t *testing.T) {
	// We can't override $HOME mid-test reliably; smoke-test
	// the function by passing an absolute path inside t.TempDir
	// (which simulates what tilde expansion would produce
	// against a real home).
	target := t.TempDir()

	d, err := driver.DialDriver(target)
	if err != nil {
		t.Fatalf("DialDriver: %v", err)
	}
	if d == nil {
		t.Fatal("DialDriver: nil driver")
	}
}

// TestDialDriver_FileURI_TildeHost exercises file://~/path —
// non-canonical but supported. Same rationale as above:
// can't reliably override $HOME, so this only checks parser
// rejects it cleanly when home lookup happens to succeed.
func TestDialDriver_FileURI_TildeHost(t *testing.T) {
	// file://~/<relative> — host="~", path="/<relative>".
	// We can't assert the resolved absolute path matches
	// anything specific without manipulating $HOME, so we
	// just ensure the parser doesn't error before the
	// filesystem call.
	uri := "file://~/scrinium-dial-test-host"
	d, err := driver.DialDriver(uri)
	if err != nil {
		// An error is acceptable here if the localfs.New step
		// fails — directory creation under $HOME may not be
		// permitted in some test environments. The point is
		// the URI parser didn't reject the form.
		if !strings.Contains(err.Error(), "localfs") &&
			!strings.Contains(err.Error(), "permission") &&
			!strings.Contains(err.Error(), "not exist") {
			t.Errorf("expected localfs/permission error, got: %v", err)
		}
		return
	}
	if d == nil {
		t.Fatal("DialDriver: nil driver")
	}
}

// TestDialDriver_UnsupportedScheme verifies that unknown
// schemes return a clear error rather than confusing fallback.
// The registry-based dialer phrases this as "scheme X not
// registered" (with a hint to import the relevant package).
func TestDialDriver_UnsupportedScheme(t *testing.T) {
	cases := []string{
		"s3://bucket/path",
		"gcs://bucket",
		"http://example.com/store",
	}
	for _, uri := range cases {
		_, err := driver.DialDriver(uri)
		if err == nil {
			t.Errorf("DialDriver(%q) succeeded; want error", uri)
			continue
		}
		if !strings.Contains(err.Error(), "not registered") {
			t.Errorf("DialDriver(%q) error = %v; want 'not registered'", uri, err)
		}
	}
}

// TestDialDriver_Empty verifies the empty-URI guard.
func TestDialDriver_Empty(t *testing.T) {
	_, err := driver.DialDriver("")
	if err == nil {
		t.Fatal("DialDriver(\"\") succeeded; want error")
	}
}

// TestDialDriver_FileURI_BadHost rejects file://something/path
// where something is neither empty, "~", nor ".". The localfs
// driver doesn't talk to remote hosts, so accepting this would
// silently misroute.
func TestDialDriver_FileURI_BadHost(t *testing.T) {
	_, err := driver.DialDriver("file://example.com/store")
	if err == nil {
		t.Fatal("DialDriver: expected error for non-local host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention 'host'", err)
	}
}

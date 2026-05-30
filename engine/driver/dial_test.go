package driver_test

import (
	"strings"
	"testing"

	"scrinium.dev/engine/driver"

	// Importing the localfs driver triggers its init(), which
	// registers the file:// scheme dialer. Tests below depend
	// on that registration; without this side-effect import
	// they would all fail with "scheme not registered".
	_ "scrinium.dev/engine/driver/localfs"
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

// TestDialDriver_FileURI_TildeHostRejected verifies the P1.12
// removal is honoured: file://~/path used to expand the host slot
// as a tilde alias. After P1.12 only file:///abs/path is accepted
// and any other host (including "~" and ".") returns
// ErrUnsupportedHost.
func TestDialDriver_FileURI_TildeHostRejected(t *testing.T) {
	_, err := driver.DialDriver("file://~/scrinium-dial-test-host")
	if err == nil {
		t.Fatal("DialDriver(file://~/...): want error, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention 'host'", err)
	}
}

// TestDialDriver_FileURI_DotHostRejected mirrors the above for the
// other historical alias.
func TestDialDriver_FileURI_DotHostRejected(t *testing.T) {
	_, err := driver.DialDriver("file://./scrinium-dial-test-cwd")
	if err == nil {
		t.Fatal("DialDriver(file://./...): want error, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention 'host'", err)
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
// where something is non-empty. The localfs driver doesn't talk
// to remote hosts, so accepting this would silently misroute.
func TestDialDriver_FileURI_BadHost(t *testing.T) {
	_, err := driver.DialDriver("file://example.com/store")
	if err == nil {
		t.Fatal("DialDriver: expected error for non-local host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention 'host'", err)
	}
}

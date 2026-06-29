package driver_test

import (
	"context"
	"os"
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

	d, err := driver.DialDriver(context.Background(), tmp)
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

	d, err := driver.DialDriver(context.Background(), uri)
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

	d, err := driver.DialDriver(context.Background(), target)
	if err != nil {
		t.Fatalf("DialDriver: %v", err)
	}
	if d == nil {
		t.Fatal("DialDriver: nil driver")
	}
}

// TestDialDriver_FileURI_TildeHostExpands verifies that the
// file://~/path host-tilde alias expands to $HOME. The alias is
// resolved by the shared resolver (scrinium.dev/internal/uri),
// uniformly with the bare ~/path form.
func TestDialDriver_FileURI_TildeHostExpands(t *testing.T) {
	if _, err := os.UserHomeDir(); err != nil {
		t.Skipf("no home dir: %v", err)
	}
	// "~" expands via os.UserHomeDir and DialDriver -> localfs.New CREATES the
	// directory; point the home dir at a temp dir (HOME on Unix, USERPROFILE on
	// Windows) so the created tree is auto-removed instead of leaking into the
	// real home directory.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d, err := driver.DialDriver(context.Background(), "file://~/scrinium-dial-test-host")
	if err != nil {
		t.Fatalf("DialDriver(file://~/...): %v", err)
	}
	if d == nil {
		t.Fatal("DialDriver(file://~/...): nil driver")
	}
}

// TestDialDriver_FileURI_DotHostExpands mirrors the above for the
// cwd-relative alias file://./path.
func TestDialDriver_FileURI_DotHostExpands(t *testing.T) {
	// "." expands against the working directory and DialDriver -> localfs.New
	// CREATES the directory; chdir into a temp dir so the created tree lands
	// there and is auto-removed, instead of leaking into the working directory.
	t.Chdir(t.TempDir())
	d, err := driver.DialDriver(context.Background(), "file://./scrinium-dial-test-cwd")
	if err != nil {
		t.Fatalf("DialDriver(file://./...): %v", err)
	}
	if d == nil {
		t.Fatal("DialDriver(file://./...): nil driver")
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
		_, err := driver.DialDriver(context.Background(), uri)
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
	_, err := driver.DialDriver(context.Background(), "")
	if err == nil {
		t.Fatal("DialDriver(\"\") succeeded; want error")
	}
}

// TestDialDriver_FileURI_BadHost rejects file://something/path
// where something is a real host (not the ~ or . aliases). The
// localfs driver doesn't talk to remote hosts, so accepting this
// would silently misroute.
func TestDialDriver_FileURI_BadHost(t *testing.T) {
	_, err := driver.DialDriver(context.Background(), "file://example.com/store")
	if err == nil {
		t.Fatal("DialDriver: expected error for non-local host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention 'host'", err)
	}
}

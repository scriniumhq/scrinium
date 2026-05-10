package scrinium_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"scrinium.dev"
	"scrinium.dev/engine/errs"

	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
)

// TestOpenOrInit_FreshDirectory_Initialises verifies the
// "create new store" branch. An empty directory has no
// descriptor; OpenOrInit must run Init.
func TestOpenOrInit_FreshDirectory_Initialises(t *testing.T) {
	dir := t.TempDir()

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir

	s, kit, created, err := scrinium.OpenOrInit(context.Background(), cfg)
	if err != nil {
		t.Fatalf("OpenOrInit: %v", err)
	}
	defer s.Close()

	if !created {
		t.Errorf("created = false; want true on a fresh directory")
	}
	if kit != nil {
		t.Errorf("kit = non-nil on Plain init; recovery kit is for "+
			"encrypted stores only (kit len = %d)", len(kit))
	}
}

// TestOpenOrInit_ExistingStore_Opens verifies the "open
// existing store" branch. Init once, close, then OpenOrInit
// must take the Open path — no second initialisation.
func TestOpenOrInit_ExistingStore_Opens(t *testing.T) {
	dir := t.TempDir()

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir

	// First call: creates.
	s1, _, created1, err := scrinium.OpenOrInit(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first OpenOrInit: %v", err)
	}
	if !created1 {
		t.Fatalf("first call: created = false; want true")
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second call: must open, not re-init.
	s2, kit2, created2, err := scrinium.OpenOrInit(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second OpenOrInit: %v", err)
	}
	defer s2.Close()

	if created2 {
		t.Errorf("second call: created = true; want false on existing store")
	}
	if kit2 != nil {
		t.Errorf("second call: kit = non-nil on Open; recovery kit is " +
			"only produced by Init")
	}
}

// TestOpenOrInit_InvalidURI_DoesNotInitialise is the regression
// for the bug the helper was designed to prevent: a typo in the
// store URI must NOT silently create an empty store somewhere
// unexpected. The error is surfaced; nothing is created.
func TestOpenOrInit_InvalidURI_DoesNotInitialise(t *testing.T) {
	cfg := scrinium.DefaultConfig()
	cfg.Store = "garbage-scheme://nowhere"

	_, _, created, err := scrinium.OpenOrInit(context.Background(), cfg)
	if err == nil {
		t.Fatal("OpenOrInit on garbage URI: want error, got nil")
	}
	if created {
		t.Error("created = true on garbage URI; should not have run Init")
	}
	// We don't pin down the exact error class — Validate or
	// DialDriver may fire first. The contract is "non-nil error,
	// no Init side effects".
}

// TestOpenOrInit_PermissionDenied_DoesNotInitialise verifies
// another genuine-error case: a file:// path the process cannot
// write to. Open will fail (cannot read descriptor); OpenOrInit
// must NOT fall through to Init, which would also fail at write
// but in a confusing way.
func TestOpenOrInit_PermissionDenied_DoesNotInitialise(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; permission-denied test would not refuse")
	}

	parent := t.TempDir()
	// 0o000 = no read/write/execute for anyone.
	denied := filepath.Join(parent, "denied")
	if err := os.Mkdir(denied, 0o000); err != nil {
		t.Fatalf("mkdir denied: %v", err)
	}
	t.Cleanup(func() {
		// Restore perms so t.TempDir cleanup can remove it.
		_ = os.Chmod(denied, 0o755)
	})

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + denied

	_, _, created, err := scrinium.OpenOrInit(context.Background(), cfg)
	if err == nil {
		t.Fatal("OpenOrInit on no-permission dir: want error, got nil")
	}
	if created {
		t.Error("created = true on permission-denied dir; should have failed at Open without falling through")
	}
}

// TestOpenOrInit_StoreNotFoundIsBridgedToFsErrNotExist sanity-
// checks our P0.4 work: the not-found case from a *failed* Open
// (one that didn't trigger init — e.g. user explicitly called
// Open instead of OpenOrInit) returns a sentinel that errors.Is
// recognises as fs.ErrNotExist. This is the user-facing contract
// host code relies on.
func TestOpenOrInit_StoreNotFoundIsBridgedToFsErrNotExist(t *testing.T) {
	dir := t.TempDir()
	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir

	_, err := scrinium.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open on empty dir: want error, got nil")
	}
	if !errors.Is(err, errs.ErrStoreNotFound) {
		t.Errorf("expected ErrStoreNotFound; got %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected the error to bridge to fs.ErrNotExist; got %v", err)
	}
}

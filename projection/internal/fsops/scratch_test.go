package fsops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// reapStaleScratch removes only scratch files older than the TTL, leaving
// a live peer's in-progress scratch (younger than the TTL) untouched.
func TestReapStaleScratch_RemovesStaleKeepsFresh(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, "scratch-stale.tmp")
	fresh := filepath.Join(dir, "scratch-fresh.tmp")
	writeScratch(t, stale)
	writeScratch(t, fresh)

	// Age the stale file well past the TTL; leave the fresh one current.
	old := time.Now().Add(-2 * staleScratchTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	reapStaleScratch(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale scratch survived: stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh scratch was reaped: %v", err)
	}
}

// The reaper matches only the scratch-*.tmp pattern and never touches
// directories, even ones whose name happens to match the glob.
func TestReapStaleScratch_IgnoresNonScratchAndDirs(t *testing.T) {
	dir := t.TempDir()

	other := filepath.Join(dir, "keepme.txt")       // wrong name
	subdir := filepath.Join(dir, "scratch-sub.tmp") // matches glob but is a dir
	writeScratch(t, other)
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := time.Now().Add(-2 * staleScratchTTL)
	mustChtimes(t, other, old)
	mustChtimes(t, subdir, old)

	reapStaleScratch(dir)

	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-scratch file removed: %v", err)
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Errorf("directory matching glob removed: %v", err)
	}
}

// An empty dir argument short-circuits: the shared OS temp dir (the
// newScratchFile fallback) must never be swept.
func TestReapStaleScratch_EmptyDirIsNoop(t *testing.T) {
	reapStaleScratch("") // must not panic and must do nothing
}

// A configured-but-absent dir yields no glob matches and no error.
func TestReapStaleScratch_MissingDir(t *testing.T) {
	reapStaleScratch(filepath.Join(t.TempDir(), "does-not-exist"))
}

func writeScratch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustChtimes(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

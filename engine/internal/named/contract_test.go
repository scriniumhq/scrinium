package named

import (
	"context"
	"path"
	"strings"
	"sync"
	"testing"
)

// contract_test.go — the two contracts the example-based tests don't reach:
// the concurrency guarantee ClaimVersion's CAS-loop exists to provide, and
// the path-safety invariant ValidateName promises. Helpers (newDriver,
// testHashes) live in namedio_test.go.

// TestClaimVersion_Concurrent drives many writers at one name simultaneously.
// ClaimVersion's only synchronisation is the driver's exclusive create, so the
// contract is: every writer gets a distinct seq, the claimed set is exactly
// 1..N with no gaps or duplicates, and the active version ends at N.
func TestClaimVersion_Concurrent(t *testing.T) {
	drv := newDriver(t)
	ctx := context.Background()
	const name = "scrub/cursor"
	const writers = 16

	body, _, err := BuildInlineManifest([]byte("claim"), "sha256", testHashes{})
	if err != nil {
		t.Fatalf("BuildInlineManifest: %v", err)
	}

	var wg sync.WaitGroup
	seqs := make([]uint64, writers)
	claimErrs := make([]error, writers)
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			seq, _, e := ClaimVersion(ctx, drv, name, body)
			seqs[i], claimErrs[i] = seq, e
		}(i)
	}
	wg.Wait()

	for i, e := range claimErrs {
		if e != nil {
			t.Fatalf("writer %d: ClaimVersion: %v", i, e)
		}
	}

	// Distinct seqs, forming exactly {1, ..., writers}.
	seen := make(map[uint64]bool, writers)
	for _, s := range seqs {
		if seen[s] {
			t.Fatalf("seq %d claimed by two writers (seqs=%v)", s, seqs)
		}
		seen[s] = true
	}
	for s := uint64(1); s <= writers; s++ {
		if !seen[s] {
			t.Fatalf("claimed set missing seq %d (seqs=%v)", s, seqs)
		}
	}

	active, found, err := ResolveActiveSeq(ctx, drv, name)
	if err != nil || !found {
		t.Fatalf("ResolveActiveSeq: found=%v err=%v", found, err)
	}
	if active != writers {
		t.Errorf("active seq = %d, want %d", active, writers)
	}
}

// FuzzValidateName asserts the path-safety contract: any name ValidateName
// accepts yields version and cell paths that stay under the system root even
// after path.Clean — an accepted name can never traverse out of "named/".
// (Rejected names make no promise and are skipped.)
func FuzzValidateName(f *testing.F) {
	for _, s := range []string{
		"config", "scrub/cursor", "a/b/c", "index_checkpoint/0",
		"", "/x", "x/", "a//b", "../x", "a/../b", ".", "a/.", "..",
		"a.b", "a.b/c", "name.with.dots", "....", "a/..b",
	} {
		f.Add(s)
	}

	const rootSlash = "named/"
	f.Fuzz(func(t *testing.T, name string) {
		if err := ValidateName(name); err != nil {
			return // rejected: no claim
		}
		vp, err := VersionPath(name, 1)
		if err != nil {
			t.Fatalf("VersionPath rejected an accepted name %q: %v", name, err)
		}
		cp, err := CellPath(name)
		if err != nil {
			t.Fatalf("CellPath rejected an accepted name %q: %v", name, err)
		}
		for _, p := range []string{vp, cp} {
			if !strings.HasPrefix(p, rootSlash) {
				t.Fatalf("path %q escapes root for accepted name %q", p, name)
			}
			if cleaned := path.Clean(p); !strings.HasPrefix(cleaned, rootSlash) {
				t.Fatalf("cleaned path %q escapes root for accepted name %q (raw %q)", cleaned, name, p)
			}
		}
	})
}

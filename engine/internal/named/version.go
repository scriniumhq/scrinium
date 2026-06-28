package named

// version.go — the versioned-file layout for system artifacts. A version is
// a flat, immutable file "named/<name>.<seq>" with a fixed-width seq.
// Publishing claims the next seq with an exclusive create (ClaimVersion); the
// active version is the highest seq present. Pruning and deletion operate on
// the seq files directly — there is no pointer to update.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

const (
	// seqWidth zero-pads the seq component to a fixed width. Active
	// selection compares seq numerically, so the width is the recognised
	// seq-file length (parseSeq is strict on it), not a sort key. 10
	// digits (up to 10^10 writes) is far beyond any system artifact's
	// version count: config is rare, checkpoints keep one version each.
	seqWidth = 10

	// maxClaimAttempts bounds the claim retry loop. Each lost race
	// against another writer consumes one attempt; the bound turns
	// pathological contention (or a buggy driver that never reports the
	// winning write) into a typed error instead of a spin. Generous:
	// real contention on a single system name is rare and resolves in
	// one retry.
	maxClaimAttempts = 64
)

// Active is one name's active (max-seq) version, as reported by ListActive.
type Active struct {
	// Name is the slash-separated system-artifact name.
	Name string
	// Seq is the active (highest) sequence number for Name.
	Seq uint64
	// Path is the driver path of the active version file.
	Path string
}

// VersionPath returns the driver path of a specific version of name: the flat
// file "named/<name>.<seq>" — no per-artifact subdirectory. Used both to
// claim a new seq and to read a known one.
func VersionPath(name string, seq uint64) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return rootSlash + name + "." + formatSeq(seq), nil
}

// formatSeq renders a seq as a fixed-width, zero-padded decimal so
// lexicographic order matches numeric order.
func formatSeq(seq uint64) string {
	return fmt.Sprintf("%0*d", seqWidth, seq)
}

// parseSeq reads a seq back from a key's trailing leaf. It is deliberately
// strict — exactly seqWidth decimal digits — so non-version entries (a deeper
// key under the same prefix, a stray file) parse as not-a-seq and are skipped
// by the keyspace scan rather than mistaken for a version.
func parseSeq(leaf string) (uint64, bool) {
	if len(leaf) != seqWidth {
		return 0, false
	}
	n, err := strconv.ParseUint(leaf, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// scanSeqs returns every version seq present for name. Versions are flat files
// "named/<name>.<seq>", so it walks the named root and keeps the entries whose
// path, relative to the root, is exactly name + '.' + a seq leaf. The root is
// small (a handful of system artifacts), so the full walk is cheap. An absent
// root yields no seqs (not an error).
func scanSeqs(ctx context.Context, drv driver.Driver, name string) ([]uint64, error) {
	fullPfx := rootSlash + name + "."
	var seqs []uint64
	err := drv.ListObjectsWithModTime(ctx, root, time.Time{}, func(o driver.ObjectMeta) error {
		if !strings.HasPrefix(o.Path, fullPfx) {
			return nil
		}
		// The remainder after "<name>." must be exactly a seq leaf; a
		// cell ("cell") or a longer-named artifact's suffix fails parseSeq.
		if n, ok := parseSeq(o.Path[len(fullPfx):]); ok {
			seqs = append(seqs, n)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("system artifact %q: scan versions: %w", name, err)
	}
	return seqs, nil
}

// ResolveActiveSeq reports the highest seq present for name (its active
// version). found is false when no version file exists — i.e. the name has
// never been written.
func ResolveActiveSeq(ctx context.Context, drv driver.Driver, name string) (seq uint64, found bool, err error) {
	if err := ValidateName(name); err != nil {
		return 0, false, err
	}
	seqs, err := scanSeqs(ctx, drv, name)
	if err != nil {
		return 0, false, err
	}
	for _, n := range seqs {
		if !found || n > seq {
			seq, found = n, true
		}
	}
	return seq, found, nil
}

// ListVersions returns every version seq of name in ascending order. An absent
// name yields an empty slice (not an error). Used by callers that need the
// full version history (e.g. ConfigHistory).
func ListVersions(ctx context.Context, drv driver.Driver, name string) ([]uint64, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	seqs, err := scanSeqs(ctx, drv, name)
	if err != nil {
		return nil, err
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

// ClaimVersion writes body as the next version of name and returns the claimed
// seq and its driver path. It is the publish step: read max(seq), try to
// create max+1 exclusively, and on a lost race (errs.ErrAlreadyExists) re-read
// and retry. The exclusive create is the only synchronisation — no lock, no
// pointer — so concurrent writers to the same name serialise on the substrate.
func ClaimVersion(ctx context.Context, drv driver.Driver, name string, body []byte) (uint64, string, error) {
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, "", fmt.Errorf("system artifact %q: claim aborted: %w", name, err)
		}
		active, found, err := ResolveActiveSeq(ctx, drv, name)
		if err != nil {
			return 0, "", err
		}
		next := uint64(1)
		if found {
			next = active + 1
		}
		path, err := VersionPath(name, next)
		if err != nil {
			return 0, "", err
		}
		err = drv.Put(ctx, path, bytes.NewReader(body), driver.WithExclusive())
		switch {
		case err == nil:
			return next, path, nil
		case errors.Is(err, errs.ErrAlreadyExists):
			// Another writer took this seq; re-read max and retry.
			continue
		default:
			return 0, "", fmt.Errorf("system artifact %q: claim seq %d: %w", name, next, err)
		}
	}
	return 0, "", fmt.Errorf("system artifact %q: %w: lost %d consecutive seq races",
		name, errs.ErrLeaseHeld, maxClaimAttempts)
}

// ListActive enumerates every system name whose name has the given prefix and
// returns each one's active (max-seq) version, sorted by name. An empty prefix
// enumerates every system name. The keyspace is planar, so the listing is a
// flat enumeration of every key under the root (names may contain slashes,
// matched as flat keys), driven by ListObjectsWithModTime, which reports files
// only and treats a missing prefix as an empty walk.
func ListActive(ctx context.Context, drv driver.Driver, prefix string) ([]Active, error) {
	best := map[string]Active{}
	fullPfx := rootSlash + prefix

	// TODO(s3): push prefix into the driver list instead of filtering in
	// memory. This passes the bare root and matches prefix here, so cost is
	// O(whole named root) per call (same in scanSeqs and ListCells). On an
	// object store this should map to ListObjectsV2(Prefix=root/prefix) →
	// O(matches), server-side. Needs the driver "prefix" arg defined as a
	// true string-prefix (localfs treats it as a dir path today) plus a
	// drivertest case for a partial-key prefix. Marginal on localfs (one
	// readdir either way); the win is the S3 backend.
	err := drv.ListObjectsWithModTime(ctx, root, time.Time{}, func(o driver.ObjectMeta) error {
		if !strings.HasPrefix(o.Path, fullPfx) {
			return nil // outside the requested name prefix
		}
		rel := o.Path[len(rootSlash):]
		if len(rel) == 0 {
			return nil
		}
		name, leaf, ok := splitLeaf(rel)
		if !ok {
			return nil
		}
		seq, ok := parseSeq(leaf)
		if !ok {
			return nil // a cell, or a stray object
		}
		if cur, seen := best[name]; !seen || seq > cur.Seq {
			best[name] = Active{Name: name, Seq: seq, Path: o.Path}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("system artifact: list %q: %w", prefix, err)
	}

	out := make([]Active, 0, len(best))
	for _, a := range best {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Prune removes all but the newest keep versions of name. Best-effort: it is
// GC, not a correctness step — a leftover old version is invisible (max(seq)
// is unaffected) and reclaimed on the next prune. keep < 1 is treated as 1
// (the active version is never a prune target). Removal failures are returned
// joined so a caller that logs them can, but no single failure aborts the rest.
func Prune(ctx context.Context, drv driver.Driver, name string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	seqs, err := ListVersions(ctx, drv, name)
	if err != nil {
		return err
	}
	if len(seqs) <= keep {
		return nil
	}
	// seqs is ascending; drop everything except the top `keep`.
	var failures []error
	for _, seq := range seqs[:len(seqs)-keep] {
		path, err := VersionPath(name, seq)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := drv.Remove(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(failures...)
}

// RemoveAll deletes every version of name (idempotent: an absent name is a
// no-op). It is the layout-level delete — there is no pointer to clear first,
// so removing the version files removes the artifact.
func RemoveAll(ctx context.Context, drv driver.Driver, name string) error {
	seqs, err := ListVersions(ctx, drv, name)
	if err != nil {
		return err
	}
	var failures []error
	for _, seq := range seqs {
		path, err := VersionPath(name, seq)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := drv.Remove(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(failures...)
}

// splitLeaf splits a flat artifact filename "<name>.<leaf>" at its LAST '.'
// into the (dotted) name and the trailing leaf — a seq or the cell marker.
// The name itself may contain dots; only the final segment is the leaf. ok is
// false when there is no dot.
func splitLeaf(rel string) (name, leaf string, ok bool) {
	i := strings.LastIndexByte(rel, '.')
	if i < 0 {
		return "", "", false
	}
	return rel[:i], rel[i+1:], true
}

// Package namedstore is the pointer-free on-disk layout for system
// artifacts (ADR-85). It is the single source of truth for where a
// system artifact lives and how its active version is chosen, shared by
// the two callers that previously each carried their own copy of the
// rule: the SystemStore facade (engine/store) and the bootstrap config
// path (engine/store/internal/storeconfig). Putting the layout here is
// what collapses that duplication into one mechanism.
//
// A system artifact is identified by a NAME — a slash-separated,
// path-like token: "config", "scrub/cursor", "index_checkpoint/<ts>".
// The name maps deterministically to a driver directory; each write of
// that name lands in a new, monotonically increasing SEQ file inside
// that directory:
//
//	system/<name>/<seq>     e.g. system/config/00000000000000000003
//	                             system/scrub/cursor/00000000000000000012
//
// The file at <name>/<seq> IS the (inline) manifest — system artifacts
// are short and unique per write, so they carry their payload inline
// with an empty Pipeline (ContentHash == BlobRef). There is no separate
// blob file, no content-addressed manifests/ entry, and no StoreIndex
// row: system artifacts live ONLY here, in their own address space.
//
// This replaces the previous mutable-pointer model (a "<name> → digest"
// file plus a content-addressed manifest). Dropping the pointer has
// three consequences:
//
//   - Active version = max(seq), discovered by reading the directory.
//     No pointer to flip, so no window in which the pointer and the
//     file it names disagree. (This is already the StoreConfig
//     activation model — see the concurrency model §3.1 — so the
//     config path stops being a special case.)
//   - A new version is published by CLAIMING the next seq with an
//     exclusive create (driver.WithExclusive — the Layer-1 atomic
//     commit primitive). Two racing writers cannot occupy one seq: the
//     loser gets errs.ErrAlreadyExists and re-reads max(seq).
//   - Rollback is "write a copy as the new max(seq)"; GC is keep-N by
//     version (Prune), never by ref-count — system artifacts are
//     outside the content-addressed GC regime.
//
// Integrity: with no index row to drive the scrub schedule, system
// artifacts are verified ON READ. Load re-hashes the inline payload
// against the manifest's embedded ContentHash, so a silently corrupted
// file is rejected at the point of use without any background scrub
// pass. Config is read at every store-open and the few other system
// names on each touch — frequent enough that verify-on-read is the
// whole integrity story.
package namedstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

const (
	// root is the single driver-side root for all system artifacts. The
	// name's first segment ("config", "scrub", "index_checkpoint", ...)
	// categorises the artifact (ADR-85: category is a name prefix, not a
	// separate namespace), so the old system.config/system.state split
	// is gone — everything hangs off this one root.
	root = "system"

	// seqWidth zero-pads the seq component to a fixed width so the
	// driver's lexicographic directory order equals numeric order:
	// max(seq) is then "the lexically last entry". 20 digits holds the
	// full uint64 range (math.MaxUint64 is 20 digits).
	seqWidth = 20

	// maxClaimAttempts bounds the claim retry loop. Each lost race
	// against another writer consumes one attempt; the bound turns
	// pathological contention (or a buggy driver that never reports the
	// winning write) into a typed error instead of a spin. Generous:
	// real contention on a single system name is rare and resolves in
	// one retry.
	maxClaimAttempts = 64

	// sessionID is the fixed SessionID stamped on system inline
	// manifests. System artifacts are addressed by name+seq, not by a
	// user write session, and are never RollbackSession targets; the
	// sentinel value is retained only because the manifest body carries
	// a session field.
	sessionID = domain.SessionID("init")
)

// Active is one name's active (max-seq) version, as reported by
// ListActive.
type Active struct {
	// Name is the slash-separated system-artifact name.
	Name string
	// Seq is the active (highest) sequence number for Name.
	Seq uint64
	// Path is the driver path of the active version file.
	Path string
}

// ValidateName enforces the name contract. Names are slash-separated,
// path-like strings: non-empty, no leading or trailing slash, no empty
// segments, no "." or ".." traversal segments. The first segment
// categorises the artifact; subsequent segments are caller-defined. A
// validated name cannot escape the system root.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", errs.ErrInvalidSystemName)
	}
	if name[0] == '/' || name[len(name)-1] == '/' {
		return fmt.Errorf("%w: %q has leading or trailing slash", errs.ErrInvalidSystemName, name)
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("%w: %q has empty segment", errs.ErrInvalidSystemName, name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q has traversal segment", errs.ErrInvalidSystemName, name)
		}
	}
	return nil
}

// dir returns the driver directory holding every version of name.
func dir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return root + "/" + name, nil
}

// VersionPath returns the driver path of a specific version of name.
// Used both to claim a new seq and to read a known one.
func VersionPath(name string, seq uint64) (string, error) {
	d, err := dir(name)
	if err != nil {
		return "", err
	}
	return d + "/" + formatSeq(seq), nil
}

// formatSeq renders a seq as a fixed-width, zero-padded decimal so
// lexicographic order matches numeric order.
func formatSeq(seq uint64) string {
	return fmt.Sprintf("%0*d", seqWidth, seq)
}

// parseSeq reads a seq back from a directory leaf. It is deliberately
// strict — exactly seqWidth decimal digits — so non-version entries (a
// nested name's subdirectory, a stray file) parse as not-a-seq and are
// skipped by the directory scan rather than mistaken for a version.
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

// ResolveActiveSeq reports the highest seq present for name (its active
// version). found is false when the directory is absent or holds no
// version files — i.e. the name has never been written.
//
// The scan tolerates a mixed directory: for a shallow name like "scrub"
// the directory may hold both seq files (versions of "scrub") and a
// subdirectory (versions of "scrub/cursor"). Only entries that parse as
// a seq count; everything else is ignored.
func ResolveActiveSeq(ctx context.Context, drv driver.Driver, name string) (seq uint64, found bool, err error) {
	d, err := dir(name)
	if err != nil {
		return 0, false, err
	}
	entries, err := drv.List(ctx, d)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("system artifact %q: list versions: %w", name, err)
	}
	for _, e := range entries {
		n, ok := parseSeq(leafOf(e))
		if !ok {
			continue
		}
		if !found || n > seq {
			seq, found = n, true
		}
	}
	return seq, found, nil
}

// ListVersions returns every version seq of name in ascending order.
// An absent name yields an empty slice (not an error). Used by callers
// that need the full version history (e.g. ConfigHistory).
func ListVersions(ctx context.Context, drv driver.Driver, name string) ([]uint64, error) {
	d, err := dir(name)
	if err != nil {
		return nil, err
	}
	entries, err := drv.List(ctx, d)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("system artifact %q: list versions: %w", name, err)
	}
	var seqs []uint64
	for _, e := range entries {
		if n, ok := parseSeq(leafOf(e)); ok {
			seqs = append(seqs, n)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

// ClaimVersion writes body as the next version of name and returns the
// claimed seq and its driver path. It is the publish step: read
// max(seq), try to create max+1 exclusively, and on a lost race
// (errs.ErrAlreadyExists) re-read and retry. The exclusive create is the
// only synchronisation — no lock, no pointer — so concurrent writers to
// the same name serialise on the substrate.
func ClaimVersion(ctx context.Context, drv driver.Driver, name string, body []byte) (uint64, string, error) {
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
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

// BuildInlineManifest constructs the encoded inline manifest for a
// system payload: an Inline blob manifest with an empty Pipeline
// (ContentHash == BlobRef == hash(payload)), serialised to the bytes
// that ClaimVersion writes. It returns the encoded file bytes and the
// in-memory manifest. No disk write and no indexing happen here — the
// caller writes the bytes through ClaimVersion.
//
// The serialised manifest's own digest is not the address (the address
// is name+seq), so it is computed only as a byproduct of encoding and
// discarded.
func BuildInlineManifest(payload []byte, hashAlgo string, hashes domain.HashRegistry) ([]byte, domain.Manifest, error) {
	hasher, err := hashes.NewHasher(hashAlgo)
	if err != nil {
		return nil, domain.Manifest{}, fmt.Errorf("system artifact: content hasher: %w", err)
	}
	if _, err := hasher.Write(payload); err != nil {
		return nil, domain.Manifest{}, fmt.Errorf("system artifact: hash payload: %w", err)
	}
	contentHash := domain.ContentHash(hex.EncodeToString(hasher.Sum(nil)))

	m := domain.Manifest{
		SessionID:    sessionID,
		ContentHash:  contentHash,
		BlobRefs:     []domain.BlobRef{domain.BlobRef(contentHash)},
		OriginalSize: int64(len(payload)),
		InlineBlob:   payload,
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutInline},
		CreatedAt:    time.Now().UTC(),
	}

	_, fileBytes, m, err := artifact.ComputeManifestDigest(
		m, hashAlgo, hashes,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain,
		nil, "")
	if err != nil {
		return nil, domain.Manifest{}, fmt.Errorf("system artifact: encode: %w", err)
	}
	return fileBytes, m, nil
}

// Load reads, decodes, and verifies the manifest at a known version
// path. Verification re-hashes the inline payload against the manifest's
// embedded ContentHash (verify-on-read): there is no external digest to
// check against — the path is name+seq, not a content hash — so the
// manifest's self-described content hash is the integrity anchor. A
// missing file maps to errs.ErrArtifactNotFound.
func Load(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, path string) (domain.Manifest, error) {
	rc, err := drv.Get(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Manifest{}, errs.ErrArtifactNotFound
		}
		return domain.Manifest{}, fmt.Errorf("system artifact: get %q: %w", path, err)
	}
	defer rc.Close()

	fileBytes, err := io.ReadAll(io.LimitReader(rc, domain.MaxManifestSize+1))
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("system artifact: read %q: %w", path, err)
	}
	if len(fileBytes) > domain.MaxManifestSize {
		return domain.Manifest{}, fmt.Errorf("system artifact %q: %w", path, errs.ErrManifestTooLarge)
	}
	m, err := artifact.Decode(fileBytes)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("system artifact: decode %q: %w", path, err)
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		return domain.Manifest{}, fmt.Errorf("system artifact %q: expected inline layout, got %q",
			path, m.LayoutHeader.BlobStorage)
	}
	if err := verifyInlinePayload(m, hashes); err != nil {
		return domain.Manifest{}, fmt.Errorf("system artifact %q: %w", path, err)
	}
	return m, nil
}

// verifyInlinePayload recomputes the content hash of an inline
// manifest's payload and checks it against the manifest's declared
// ContentHash. For a system artifact the Pipeline is empty, so the
// content hash is just hash(InlineBlob).
func verifyInlinePayload(m domain.Manifest, hashes domain.HashRegistry) error {
	// ADR-93: ContentHash is bare hex; the algorithm is the manifest's
	// recorded hash_algo (populated on decode, which precedes this check).
	h, err := hashes.NewHasher(m.HashAlgo)
	if err != nil {
		return fmt.Errorf("%w: content hasher: %v", errs.ErrCorruptedContent, err)
	}
	if _, err := h.Write(m.InlineBlob); err != nil {
		return fmt.Errorf("%w: hash payload: %v", errs.ErrCorruptedContent, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != string(m.ContentHash) {
		return fmt.Errorf("%w: payload hash %s != declared %s", errs.ErrCorruptedContent, got, m.ContentHash)
	}
	return nil
}

// ListActive enumerates every system name whose name has the given
// prefix and returns each one's active (max-seq) version, sorted by
// name. An empty prefix enumerates every system name. The listing is
// recursive (names are nested), driven by ListObjectsWithModTime, which
// reports files only and treats a missing prefix as an empty walk.
func ListActive(ctx context.Context, drv driver.Driver, prefix string) ([]Active, error) {
	listPath := root + "/" + prefix
	rootSlash := root + "/"

	best := map[string]Active{}
	err := drv.ListObjectsWithModTime(ctx, listPath, time.Time{}, func(o driver.ObjectMeta) error {
		rel := strings.TrimPrefix(o.Path, rootSlash)
		if rel == o.Path || rel == "" {
			return nil // path was not under the system root
		}
		leaf := leafOf(rel)
		seq, ok := parseSeq(leaf)
		if !ok {
			return nil // not a version file (a stray object under system/)
		}
		name := strings.TrimSuffix(rel, "/"+leaf)
		if name == "" || name == rel {
			return nil // a seq file directly under system/ has no name
		}
		if cur, seen := best[name]; !seen || seq > cur.Seq {
			best[name] = Active{Name: name, Seq: seq, Path: o.Path}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("system artifact: list %q: %w", listPath, err)
	}

	out := make([]Active, 0, len(best))
	for _, a := range best {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Prune removes all but the newest keep versions of name. Best-effort:
// it is GC, not a correctness step — a leftover old version is invisible
// (max(seq) is unaffected) and reclaimed on the next prune. keep < 1 is
// treated as 1 (the active version is never a prune target). Removal
// failures are returned joined so a caller that logs them can, but no
// single failure aborts the rest.
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

// RemoveAll deletes every version of name (idempotent: an absent name is
// a no-op). It is the layout-level delete — there is no pointer to clear
// first, so removing the version files removes the artifact.
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

// leafOf returns the final '/'-separated segment of a driver path or
// listing entry. Drivers may report either full paths or bare leaf names
// from List; taking the last segment normalises both.
func leafOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

package namedstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
)

// cellLeaf is the reserved trailing leaf that marks a name's keep=0
// exclusive cell (ADR-100/101): the flat file "named/<name>.cell". It is
// deliberately NOT seqWidth decimal digits, so parseSeq("cell") is false
// and ResolveActiveSeq/ListVersions skip it: a name is therefore EITHER a
// cell ("<name>.cell") OR a versioned series ("<name>.<seq>"), never both,
// and the two forms never collide on a single driver path.
//
// The cell is the keep=0 form: a single fixed slot (no seq) — the single
// point of contention a lock needs. Versions would split that point and
// break mutual exclusion, so the lock form has none.
const cellLeaf = "cell"

// CellPath returns the driver path of name's keep=0 exclusive cell.
func CellPath(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return root + "/" + name + "." + cellLeaf, nil
}

// WriteCell writes body (an encoded inline manifest from
// BuildInlineManifest) to name's cell.
//
//   - exclusive=true → create-if-absent (driver.WithExclusive): an
//     existing cell yields a wrapped errs.ErrAlreadyExists. This is the
//     atomic acquire a lock relies on — the substrate, not a pointer or
//     a lock file, serialises the contenders.
//   - exclusive=false → overwrite in place: renew/takeover by the holder,
//     or a plain keep=0 last-write-wins update.
//
// The write DISCIPLINE (when to acquire vs overwrite, TTL, takeover) is
// the caller's policy (e.g. lease.go), not the storage contract: this
// primitive only offers the two write modes over one fixed slot.
func WriteCell(ctx context.Context, drv driver.Driver, name string, body []byte, exclusive bool) error {
	path, err := CellPath(name)
	if err != nil {
		return err
	}
	var opts []driver.PutOption
	if exclusive {
		opts = append(opts, driver.WithExclusive())
	}
	if err := drv.Put(ctx, path, bytes.NewReader(body), opts...); err != nil {
		// errs.ErrAlreadyExists is preserved through %w so an acquiring
		// caller can errors.Is against it.
		return fmt.Errorf("system artifact %q: write cell (exclusive=%v): %w", name, exclusive, err)
	}
	return nil
}

// LoadCell reads, decodes, and verifies name's cell (verify-on-read via
// Load). An absent cell maps to errs.ErrArtifactNotFound.
func LoadCell(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, name string) (domain.Manifest, error) {
	path, err := CellPath(name)
	if err != nil {
		return domain.Manifest{}, err
	}
	return Load(ctx, drv, hashes, path)
}

// RemoveCell deletes name's cell. Idempotent: an absent cell is not an
// error (mirrors RemoveAll for versioned names). The (now-empty) name
// directory is left in place, as RemoveAll leaves it.
func RemoveCell(ctx context.Context, drv driver.Driver, name string) error {
	path, err := CellPath(name)
	if err != nil {
		return err
	}
	if err := drv.Remove(ctx, path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("system artifact %q: remove cell: %w", name, err)
	}
	return nil
}

// ListCells returns every keep=0 cell under prefix as Active entries
// (Seq is 0 and meaningless for a cell). It mirrors ListActive, which
// reports only versioned names and skips the "cell" leaf; SystemStore
// .Walk merges both lists so cells (e.g. the lease) are visible in the
// system view. A name is exactly one form, so the two lists never
// overlap.
func ListCells(ctx context.Context, drv driver.Driver, prefix string) ([]Active, error) {
	rootSlash := root + "/"

	var out []Active
	// Cells are flat files "named/<name>.cell". Split each entry at its
	// last '.'; keep those whose leaf is the cell marker (see ListActive).
	err := drv.ListObjectsWithModTime(ctx, root, time.Time{}, func(o driver.ObjectMeta) error {
		rel := strings.TrimPrefix(o.Path, rootSlash)
		if rel == o.Path || rel == "" {
			return nil // not under the named root
		}
		name, leaf, ok := splitLeaf(rel)
		if !ok || leaf != cellLeaf {
			return nil // not a cell file (a version, or a stray object)
		}
		if !strings.HasPrefix(name, prefix) {
			return nil // outside the requested name prefix
		}
		out = append(out, Active{Name: name, Path: o.Path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("system artifact: list cells %q: %w", prefix, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

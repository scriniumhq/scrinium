package named

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// buildCellBody is a test helper: encode payload as the inline-manifest
// bytes a cell stores (same shape ClaimVersion writes for versions).
func buildCellBody(t *testing.T, payload []byte) []byte {
	t.Helper()
	body, _, err := BuildInlineManifest("test/cell", payload, "sha256", testHashes{}, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatalf("BuildInlineManifest: %v", err)
	}
	return body
}

// TestCell_WriteExclusiveRoundTrip: an exclusive write into an empty
// cell succeeds and reads back with verify-on-read intact.
func TestCell_WriteExclusiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	payload := []byte("lease-record-v1")

	if err := WriteCell(ctx, drv, "store.agent.gc.lease", buildCellBody(t, payload), true); err != nil {
		t.Fatalf("WriteCell exclusive: %v", err)
	}
	m, err := LoadCell(ctx, drv, testHashes{}, "store.agent.gc.lease")
	if err != nil {
		t.Fatalf("LoadCell: %v", err)
	}
	if !bytes.Equal(m.InlineBlob, payload) {
		t.Errorf("InlineBlob = %q, want %q", m.InlineBlob, payload)
	}
}

// TestCell_ExclusiveOnExistingFails: the second exclusive write to an
// occupied cell fails with ErrAlreadyExists — the lock-acquire contract.
func TestCell_ExclusiveOnExistingFails(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	name := "store.agent.gc.lease"

	if err := WriteCell(ctx, drv, name, buildCellBody(t, []byte("holder-A")), true); err != nil {
		t.Fatalf("first exclusive WriteCell: %v", err)
	}
	err := WriteCell(ctx, drv, name, buildCellBody(t, []byte("holder-B")), true)
	if !errors.Is(err, errs.ErrAlreadyExists) {
		t.Fatalf("second exclusive WriteCell = %v, want ErrAlreadyExists", err)
	}
	// The original holder is untouched.
	m, err := LoadCell(ctx, drv, testHashes{}, name)
	if err != nil {
		t.Fatalf("LoadCell: %v", err)
	}
	if !bytes.Equal(m.InlineBlob, []byte("holder-A")) {
		t.Errorf("cell overwritten by failed exclusive write: got %q", m.InlineBlob)
	}
}

// TestCell_OverwriteReplaces: a non-exclusive write replaces the cell in
// place — the renew/takeover (and plain keep=0 update) path.
func TestCell_OverwriteReplaces(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	name := "store.agent.gc.lease"

	if err := WriteCell(ctx, drv, name, buildCellBody(t, []byte("A")), true); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := WriteCell(ctx, drv, name, buildCellBody(t, []byte("B")), false); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	m, err := LoadCell(ctx, drv, testHashes{}, name)
	if err != nil {
		t.Fatalf("LoadCell: %v", err)
	}
	if !bytes.Equal(m.InlineBlob, []byte("B")) {
		t.Errorf("InlineBlob = %q, want B", m.InlineBlob)
	}
}

// TestCell_LoadAbsent: loading a never-written cell is ErrArtifactNotFound.
func TestCell_LoadAbsent(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	if _, err := LoadCell(ctx, drv, testHashes{}, "store.agent.never"); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("LoadCell absent = %v, want ErrArtifactNotFound", err)
	}
}

// TestCell_RemoveIdempotent: remove deletes the cell and is a no-op when
// already absent.
func TestCell_RemoveIdempotent(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	name := "store.agent.gc.lease"

	if err := WriteCell(ctx, drv, name, buildCellBody(t, []byte("x")), true); err != nil {
		t.Fatalf("WriteCell: %v", err)
	}
	if err := RemoveCell(ctx, drv, name); err != nil {
		t.Fatalf("RemoveCell: %v", err)
	}
	if _, err := LoadCell(ctx, drv, testHashes{}, name); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("LoadCell after remove = %v, want ErrArtifactNotFound", err)
	}
	if err := RemoveCell(ctx, drv, name); err != nil {
		t.Fatalf("RemoveCell idempotent = %v, want nil", err)
	}
}

// TestCell_NotMistakenForVersion is the disjointness invariant: a cell
// written under a name is invisible to the version resolver (the "cell"
// leaf is not a seq), so the two forms never collide.
func TestCell_NotMistakenForVersion(t *testing.T) {
	ctx := context.Background()
	drv := newDriver(t)
	name := "store/config"

	if err := WriteCell(ctx, drv, name, buildCellBody(t, []byte("c")), true); err != nil {
		t.Fatalf("WriteCell: %v", err)
	}
	_, found, err := ResolveActiveSeq(ctx, drv, name)
	if err != nil {
		t.Fatalf("ResolveActiveSeq: %v", err)
	}
	if found {
		t.Errorf("ResolveActiveSeq found a version for a cell-only name; cell leaf leaked as a seq")
	}
	seqs, err := ListVersions(ctx, drv, name)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(seqs) != 0 {
		t.Errorf("ListVersions = %v, want empty for a cell-only name", seqs)
	}
}

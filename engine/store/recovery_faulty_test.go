package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver/faulty"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// faultyIndex wraps a real store.StoreIndex and injects errors into
// the two methods recoverOrphans consults: Resolve (per-blob) and
// ManifestExists (per-manifest). All other calls pass through to
// the embedded backend.
//
// Used to exercise the "transient index error" branch of
// recoverOrphans: per the contract, an index-infrastructure failure
// during a sweep must NOT cause the orphan to be removed (better
// leave a possibly-orphan blob on disk than mistake healthy data
// for orphan because of a transient SQLite hiccup).
type faultyIndex struct {
	index.StoreIndex
	resolveErr        error // if non-nil, Resolve returns this
	manifestExistsErr error // if non-nil, ManifestExists returns this
}

func (f *faultyIndex) Resolve(ctx context.Context, ref string) (domain.PhysicalAddress, error) {
	if f.resolveErr != nil {
		return domain.PhysicalAddress{}, f.resolveErr
	}
	return f.StoreIndex.Resolve(ctx, ref)
}

func (f *faultyIndex) ManifestExists(ctx context.Context, id domain.ArtifactID) (bool, error) {
	if f.manifestExistsErr != nil {
		return false, f.manifestExistsErr
	}
	return f.StoreIndex.ManifestExists(ctx, id)
}

// --- 1. Resolve returns a non-NotFound error: blob must NOT be removed.

func TestRecoverOrphans_TransientResolveError_PreservesBlob(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := &faultyIndex{
		StoreIndex: indexfx.Memory(t),
		resolveErr: errors.New("simulated SQLite busy"),
	}

	ref := "sha256-" + strings.Repeat("ab", 16) + "cd"
	blobPath := "blobs/" + ref[:4] + "/" + ref[4:8] + "/" + ref
	if err := drv.Put(context.Background(), blobPath, strings.NewReader("orphan or not?")); err != nil {
		t.Fatalf("Put blob: %v", err)
	}

	report, err := store.RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}

	if _, err := drv.Stat(context.Background(), blobPath); err != nil {
		t.Errorf("blob removed despite transient Resolve error: %v", err)
	}
	if report.BlobsRemoved != 0 {
		t.Errorf("BlobsRemoved = %d; want 0 on transient error", report.BlobsRemoved)
	}
	if len(report.Errors) == 0 {
		t.Errorf("Errors empty; want >=1 entry recording the transient failure")
	}
}

func TestRecoverOrphans_TransientResolveError_DoesNotAbortSweep(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := &faultyIndex{
		StoreIndex: indexfx.Memory(t),
		resolveErr: errors.New("transient"),
	}

	for i, suffix := range []string{"01", "02"} {
		ref := "sha256-" + strings.Repeat("cd", 16) + suffix
		path := "blobs/" + ref[:4] + "/" + ref[4:8] + "/" + ref
		if err := drv.Put(context.Background(), path, strings.NewReader("blob")); err != nil {
			t.Fatalf("blob %d: %v", i, err)
		}
	}
	stagingPath := "system.state/staging/leftover-from-crashed-put"
	if err := drv.Put(context.Background(), stagingPath, strings.NewReader("staging")); err != nil {
		t.Fatalf("staging: %v", err)
	}

	report, err := store.RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}

	if _, err := drv.Stat(context.Background(), stagingPath); err == nil {
		t.Errorf("staging file %q must be removed even when blobs sweep had errors", stagingPath)
	}
	if report.StagingRemoved != 1 {
		t.Errorf("StagingRemoved = %d; want 1", report.StagingRemoved)
	}
	if report.BlobsRemoved != 0 {
		t.Errorf("BlobsRemoved = %d; want 0", report.BlobsRemoved)
	}
	if len(report.Errors) < 2 {
		t.Errorf("Errors has %d entries; want >=2 (one per failed Resolve)", len(report.Errors))
	}
}

// --- 2. ManifestExists returns an error: manifest must NOT be removed.

func TestRecoverOrphans_TransientManifestExistsError_PreservesManifest(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := &faultyIndex{
		StoreIndex:        indexfx.Memory(t),
		manifestExistsErr: errors.New("simulated index outage"),
	}

	id := "sha256-" + strings.Repeat("ef", 16) + "00"
	manifestPath := "manifests/" + id[:4] + "/" + id[4:8] + "/" + id
	if err := drv.Put(context.Background(), manifestPath, strings.NewReader("{}")); err != nil {
		t.Fatalf("Put manifest: %v", err)
	}

	report, err := store.RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}

	if _, err := drv.Stat(context.Background(), manifestPath); err != nil {
		t.Errorf("manifest removed despite transient ManifestExists error: %v", err)
	}
	if report.ManifestsRemoved != 0 {
		t.Errorf("ManifestsRemoved = %d; want 0", report.ManifestsRemoved)
	}
	if len(report.Errors) == 0 {
		t.Errorf("Errors empty; want the transient error recorded")
	}
}

// --- 3. drv.Remove fails: file stays, error is recorded.

func TestRecoverOrphans_RemoveFails_OrphanStays(t *testing.T) {
	inner := driverfx.LocalFS(t)
	drv := faulty.New(inner,
		faulty.WithSeed(42),
		faulty.WithFailureRate(faulty.MethodRemove, 1.0),
	)

	stagingPath := "system.state/staging/leftover-from-crash"
	if err := inner.Put(context.Background(), stagingPath, strings.NewReader("x")); err != nil {
		t.Fatalf("inner.Put: %v", err)
	}

	report, err := store.RecoverOrphans(context.Background(), drv, indexfx.Memory(t))
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}

	if _, err := inner.Stat(context.Background(), stagingPath); err != nil {
		t.Errorf("staging file should remain when Remove fails: %v", err)
	}
	if report.StagingRemoved != 0 {
		t.Errorf("StagingRemoved = %d; want 0 when every Remove fails", report.StagingRemoved)
	}
	if len(report.Errors) == 0 {
		t.Errorf("Errors empty; want the injected Remove failure recorded")
	}
	var foundInjected bool
	for _, e := range report.Errors {
		if errors.Is(e, errs.ErrInjected) {
			foundInjected = true
			break
		}
	}
	if !foundInjected {
		t.Errorf("Errors did not contain errs.ErrInjected; entries=%v", report.Errors)
	}
}

// --- 4. Sanity: known-not-found path still removes the orphan.

func TestRecoverOrphans_Default_RemovesUnknownBlob(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)

	ref := "sha256-" + strings.Repeat("12", 16) + "ff"
	blobPath := "blobs/" + ref[:4] + "/" + ref[4:8] + "/" + ref
	if err := drv.Put(context.Background(), blobPath, strings.NewReader("orphan")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	report, err := store.RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}

	if _, err := drv.Stat(context.Background(), blobPath); err == nil {
		t.Errorf("orphan blob should have been removed (Resolve returns NotFound on empty index)")
	}
	if report.BlobsRemoved != 1 {
		t.Errorf("BlobsRemoved = %d; want 1", report.BlobsRemoved)
	}
}

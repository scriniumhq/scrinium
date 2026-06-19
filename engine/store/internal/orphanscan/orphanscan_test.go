package orphanscan

import (
	"context"
	"errors"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// White-box tests for the orphan-recovery sweep. RecoverOrphans is
// DESTRUCTIVE (it removes files), so the suite covers not just the happy
// removals but the two safety properties that keep it from eating live
// data: a transient index error must LEAVE a file on disk, and an empty
// index must be understood to remove everything it sees (the cold-start
// hazard, ADR/backlog §3.1).
//
// Both collaborators are hand-built fakes rather than a localfs driver +
// real index: the sweep's branches (known / orphan / transient-error /
// unparseable) are about what the index ANSWERS, and a fake answers on
// demand without standing up a database or seeding real blobs.

// --- fixtures ------------------------------------------------------------

// fakeDriver serves a seeded set of object paths per prefix and records
// every Remove. The embedded driver.Driver is left nil: RecoverOrphans
// touches only ListObjectsWithModTime and Remove, so any other call would
// panic loudly rather than pass silently on a stub.
type fakeDriver struct {
	driver.Driver
	objects   map[string][]string // prefix -> object paths under it
	listErr   map[string]error    // prefix -> error returned by List
	removeErr map[string]error    // path   -> error returned by Remove
	removed   []string
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		objects:   map[string][]string{},
		listErr:   map[string]error{},
		removeErr: map[string]error{},
	}
}

func (d *fakeDriver) ListObjectsWithModTime(ctx context.Context, prefix string, _ time.Time, cb func(driver.ObjectMeta) error) error {
	if err := d.listErr[prefix]; err != nil {
		return err
	}
	for _, p := range d.objects[prefix] {
		if err := cb(driver.ObjectMeta{Path: p}); err != nil {
			return err
		}
	}
	return nil
}

func (d *fakeDriver) Remove(_ context.Context, path string) error {
	if err := d.removeErr[path]; err != nil {
		return err
	}
	d.removed = append(d.removed, path)
	return nil
}

func (d *fakeDriver) wasRemoved(path string) bool {
	for _, r := range d.removed {
		if r == path {
			return true
		}
	}
	return false
}

// fakeIndex answers only the two StoreIndex methods RecoverOrphans
// consults; resolve / manifestExists are programmable per test so each
// branch is reachable in isolation. The embedded interface is nil for the
// same loud-failure reason as fakeDriver.
type fakeIndex struct {
	index.StoreIndex
	resolve        func(ref string) (domain.PhysicalAddress, error)
	manifestExists func(digest domain.ManifestDigest) (bool, error)
}

func (i fakeIndex) Resolve(_ context.Context, ref string) (domain.PhysicalAddress, error) {
	return i.resolve(ref)
}

func (i fakeIndex) ManifestExistsByDigest(_ context.Context, digest domain.ManifestDigest) (bool, error) {
	return i.manifestExists(digest)
}

// Canned index answers, named for readability at the call sites.
func found(string) (domain.PhysicalAddress, error) { return domain.PhysicalAddress{}, nil }
func notFound(string) (domain.PhysicalAddress, error) {
	return domain.PhysicalAddress{}, errs.ErrArtifactNotFound
}
func resolveBoom(string) (domain.PhysicalAddress, error) {
	return domain.PhysicalAddress{}, errors.New("index unavailable")
}

func manifestPresent(domain.ManifestDigest) (bool, error) { return true, nil }
func manifestMissing(domain.ManifestDigest) (bool, error) { return false, nil }
func manifestBoom(domain.ManifestDigest) (bool, error)    { return false, errors.New("index unavailable") }

// Valid sharded paths. A ref/digest is the last path segment and must be
// ≥4 lowercase-hex chars (artifact.validateRefShape).
const (
	stagingFile = domain.StagingPrefix + "/tmp-write-1234"
	blobOrphan  = "blobs/de/ad/deadbeef"
	blobKnown   = "blobs/ab/cd/abcdef01"
	blobBadPath = "blobs/ab/cd/bad" // <4 chars: RefFromBlobPath rejects it
	maniOrphan  = "manifests/de/ad/deadbeef"
	maniKnown   = "manifests/ab/cd/abcdef01"
	maniBadPath = "manifests/ab/cd/bad"
)

// --- staging sweep -------------------------------------------------------

func TestRecoverOrphans_StagingAlwaysRemoved(t *testing.T) {
	drv := newFakeDriver()
	drv.objects[domain.StagingPrefix] = []string{stagingFile, domain.StagingPrefix + "/tmp-write-5678"}
	idx := fakeIndex{resolve: notFound, manifestExists: manifestMissing}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if report.StagingRemoved != 2 {
		t.Errorf("StagingRemoved: got %d, want 2", report.StagingRemoved)
	}
	if !drv.wasRemoved(stagingFile) {
		t.Error("staging file should be removed unconditionally")
	}
	if len(report.Errors) != 0 {
		t.Errorf("Errors: got %v, want none", report.Errors)
	}
}

func TestRecoverOrphans_StagingRemoveError_CollectedAndContinues(t *testing.T) {
	first := domain.StagingPrefix + "/a"
	second := domain.StagingPrefix + "/b"
	drv := newFakeDriver()
	drv.objects[domain.StagingPrefix] = []string{first, second}
	drv.removeErr[first] = errors.New("disk gone")
	idx := fakeIndex{resolve: notFound, manifestExists: manifestMissing}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	// The failed Remove is collected, not fatal; the sweep continues.
	if report.StagingRemoved != 1 {
		t.Errorf("StagingRemoved: got %d, want 1", report.StagingRemoved)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Errors: got %d, want 1", len(report.Errors))
	}
	if !drv.wasRemoved(second) {
		t.Error("second staging file should still be removed after the first errored")
	}
}

// --- blobs sweep ---------------------------------------------------------

func TestRecoverOrphans_OrphanBlobRemoved_KnownBlobKept(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["blobs"] = []string{blobOrphan, blobKnown}
	idx := fakeIndex{
		resolve: func(ref string) (domain.PhysicalAddress, error) {
			if ref == "abcdef01" { // the known blob resolves
				return found(ref)
			}
			return notFound(ref)
		},
		manifestExists: manifestMissing,
	}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if report.BlobsRemoved != 1 {
		t.Errorf("BlobsRemoved: got %d, want 1", report.BlobsRemoved)
	}
	if !drv.wasRemoved(blobOrphan) {
		t.Error("orphan blob (unresolved ref) should be removed")
	}
	if drv.wasRemoved(blobKnown) {
		t.Error("known blob (resolved ref) must NOT be removed")
	}
}

// TestRecoverOrphans_BlobTransientIndexError_LeavesOnDisk is the
// safety-critical branch: a Resolve failure that is NOT ErrArtifactNotFound
// is index trouble, not proof of orphanhood. The file must stay — better a
// lingering orphan than deleting live data on a transient hiccup.
func TestRecoverOrphans_BlobTransientIndexError_LeavesOnDisk(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["blobs"] = []string{blobKnown}
	idx := fakeIndex{resolve: resolveBoom, manifestExists: manifestMissing}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if report.BlobsRemoved != 0 {
		t.Errorf("BlobsRemoved: got %d, want 0 (transient error must not delete)", report.BlobsRemoved)
	}
	if drv.wasRemoved(blobKnown) {
		t.Error("blob must survive a transient index error")
	}
	if len(report.Errors) != 1 {
		t.Errorf("Errors: got %d, want 1 (the resolve failure)", len(report.Errors))
	}
}

func TestRecoverOrphans_UnparseableBlobPath_SkippedWithError(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["blobs"] = []string{blobBadPath}
	// resolve must never be reached for an unparseable path.
	idx := fakeIndex{
		resolve: func(ref string) (domain.PhysicalAddress, error) {
			t.Fatalf("Resolve called for unparseable path with ref %q", ref)
			return domain.PhysicalAddress{}, nil
		},
		manifestExists: manifestMissing,
	}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if drv.wasRemoved(blobBadPath) {
		t.Error("unparseable blob path must not be removed")
	}
	if len(report.Errors) != 1 {
		t.Errorf("Errors: got %d, want 1 (the parse failure)", len(report.Errors))
	}
}

// --- manifests sweep -----------------------------------------------------

func TestRecoverOrphans_OrphanManifestRemoved_KnownManifestKept(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["manifests"] = []string{maniOrphan, maniKnown}
	idx := fakeIndex{
		resolve: notFound,
		manifestExists: func(d domain.ManifestDigest) (bool, error) {
			return d == "abcdef01", nil // the known manifest exists in the index
		},
	}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if report.ManifestsRemoved != 1 {
		t.Errorf("ManifestsRemoved: got %d, want 1", report.ManifestsRemoved)
	}
	if !drv.wasRemoved(maniOrphan) {
		t.Error("orphan manifest (absent from index) should be removed")
	}
	if drv.wasRemoved(maniKnown) {
		t.Error("indexed manifest must NOT be removed")
	}
}

// TestRecoverOrphans_ManifestExistsError_LeavesOnDisk is the manifest-side
// counterpart of the blob safety branch.
func TestRecoverOrphans_ManifestExistsError_LeavesOnDisk(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["manifests"] = []string{maniKnown}
	idx := fakeIndex{resolve: notFound, manifestExists: manifestBoom}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if report.ManifestsRemoved != 0 {
		t.Errorf("ManifestsRemoved: got %d, want 0 (index error must not delete)", report.ManifestsRemoved)
	}
	if drv.wasRemoved(maniKnown) {
		t.Error("manifest must survive a ManifestExistsByDigest error")
	}
	if len(report.Errors) != 1 {
		t.Errorf("Errors: got %d, want 1", len(report.Errors))
	}
}

func TestRecoverOrphans_UnparseableManifestPath_SkippedWithError(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["manifests"] = []string{maniBadPath}
	idx := fakeIndex{
		resolve: notFound,
		manifestExists: func(d domain.ManifestDigest) (bool, error) {
			t.Fatalf("ManifestExistsByDigest called for unparseable path, digest %q", d)
			return false, nil
		},
	}

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if drv.wasRemoved(maniBadPath) {
		t.Error("unparseable manifest path must not be removed")
	}
	if len(report.Errors) != 1 {
		t.Errorf("Errors: got %d, want 1", len(report.Errors))
	}
}

// --- cold-start hazard (pins a known sharp edge) -------------------------

// TestRecoverOrphans_EmptyIndex_RemovesEverything documents the §3.1
// hazard rather than guarding it: RecoverOrphans trusts the index as the
// sole authority, so an EMPTY index (every Resolve -> NotFound, every
// ManifestExists -> false) makes it delete every blob and manifest it
// finds. The guard belongs at the caller — the recovery sweep must NOT run
// against a populated-but-not-yet-indexed disk (e.g. a freshly reopened
// Store before the rebuild agent repopulates the index). This test will
// fail loudly if that contract ever changes, forcing a conscious decision.
func TestRecoverOrphans_EmptyIndex_RemovesEverything(t *testing.T) {
	drv := newFakeDriver()
	drv.objects["blobs"] = []string{blobOrphan, blobKnown}
	drv.objects["manifests"] = []string{maniOrphan, maniKnown}
	idx := fakeIndex{resolve: notFound, manifestExists: manifestMissing} // empty index

	report, err := RecoverOrphans(context.Background(), drv, idx)
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if report.BlobsRemoved != 2 || report.ManifestsRemoved != 2 {
		t.Fatalf("empty index should sweep all: got blobs=%d manifests=%d, want 2/2",
			report.BlobsRemoved, report.ManifestsRemoved)
	}
	for _, p := range []string{blobOrphan, blobKnown, maniOrphan, maniKnown} {
		if !drv.wasRemoved(p) {
			t.Errorf("empty index: %q should have been removed", p)
		}
	}
}

// --- abort paths ---------------------------------------------------------

func TestRecoverOrphans_ListError_AbortsWithError(t *testing.T) {
	boom := errors.New("list failed")
	drv := newFakeDriver()
	drv.listErr["blobs"] = boom
	drv.objects["blobs"] = []string{blobOrphan} // never reached
	idx := fakeIndex{resolve: notFound, manifestExists: manifestMissing}

	_, err := RecoverOrphans(context.Background(), drv, idx)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the list error to propagate, got %v", err)
	}
	if drv.wasRemoved(blobOrphan) {
		t.Error("nothing should be removed once a List aborts the sweep")
	}
}

func TestRecoverOrphans_ContextCancelled_Aborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	drv := newFakeDriver()
	drv.objects[domain.StagingPrefix] = []string{stagingFile}
	idx := fakeIndex{resolve: notFound, manifestExists: manifestMissing}

	_, err := RecoverOrphans(ctx, drv, idx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if drv.wasRemoved(stagingFile) {
		t.Error("a cancelled context must stop the sweep before any Remove")
	}
}

// --- report publishing ---------------------------------------------------

// capturePublisher records published events for assertion.
type capturePublisher struct{ events []event.Event }

func (p *capturePublisher) Publish(e event.Event) { p.events = append(p.events, e) }

func TestPublishOrphanReport_NilPublisher_NoOp(t *testing.T) {
	// Must not panic on a nil Publisher (the minimal-stack default).
	PublishOrphanReport(nil, OrphanReport{StagingRemoved: 1})
}

func TestPublishOrphanReport_EmitsCounts(t *testing.T) {
	pub := &capturePublisher{}
	report := OrphanReport{
		StagingRemoved:   1,
		BlobsRemoved:     2,
		ManifestsRemoved: 3,
		Errors:           []error{errors.New("x"), errors.New("y")},
		Duration:         5 * time.Millisecond,
	}

	PublishOrphanReport(pub, report)

	if len(pub.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.Type != event.EventOrphanScanCompleted {
		t.Errorf("event type: got %q, want %q", ev.Type, event.EventOrphanScanCompleted)
	}
	payload, ok := ev.Payload.(event.OrphanScanCompletedPayload)
	if !ok {
		t.Fatalf("payload type: got %T, want OrphanScanCompletedPayload", ev.Payload)
	}
	if payload.BlobsRemoved != 2 || payload.ManifestsRemoved != 3 || payload.StagingRemoved != 1 {
		t.Errorf("payload counts: got %+v", payload)
	}
	// The payload carries the error COUNT, not the error values.
	if payload.NonFatalErrors != 2 {
		t.Errorf("NonFatalErrors: got %d, want 2", payload.NonFatalErrors)
	}
}

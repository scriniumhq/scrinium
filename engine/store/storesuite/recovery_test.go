// Recovery: what the bootstrap orphan scan does with files left by a prior
// (crashed) process, the orphanscan transient-error contracts, and recovery
// of a lost descriptor from its Recovery Kit. Three entry points share this
// file because they are one concern (durability/recovery): Store bootstrap
// (InitStore/OpenStore run the scan and publish a report), the orphanscan
// package directly (its index-error branches), and RestoreDescriptorFromRecoveryKit.

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/faulty"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/orphanscan"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// --- shared test rig ----------------------------------------------

// recoveryFixture exposes the Driver, Index and event Recorder separately so
// individual tests can stage on-disk preconditions BEFORE calling InitStore
// or OpenStore — the whole point is exercising what the bootstrap scan does
// with files placed by a prior (crashed) process. Events are captured with
// the shared eventfx.Recorder.
type recoveryFixture struct {
	drv driver.Driver
	idx index.StoreIndex
	rec *eventfx.Recorder
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	return &recoveryFixture{
		drv: driverfx.LocalFS(t),
		idx: indexfx.Memory(t),
		rec: eventfx.New(),
	}
}

// initStore runs store.InitStore against the fixture. EventOrphanScanCompleted
// has been recorded by the time this returns.
func (f *recoveryFixture) initStore(t *testing.T) store.Store {
	t.Helper()
	s, _, err := store.InitStore(context.Background(), f.drv,
		store.WithStoreIndex(f.idx),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithPublisher(f.rec),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	return s
}

// openStore runs store.OpenStore on the same Driver+Index — for
// "crash-then-reopen" scenarios.
func (f *recoveryFixture) openStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.OpenStore(context.Background(), f.drv,
		store.WithStoreIndex(f.idx),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithPublisher(f.rec),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s
}

// stageFile plants a file with the given content at a root-relative path
// through the Driver — a synthetic orphan blob / manifest / staging file.
func (f *recoveryFixture) stageFile(t *testing.T, path, content string) {
	t.Helper()
	if err := f.drv.Put(context.Background(), path, strings.NewReader(content)); err != nil {
		t.Fatalf("Driver.Put(%q): %v", path, err)
	}
}

// fileExists probes whether the Driver still sees a path.
func (f *recoveryFixture) fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := f.drv.Stat(context.Background(), path)
	return err == nil
}

// lastReport returns the most recent EventOrphanScanCompleted payload.
func lastReport(t *testing.T, rec *eventfx.Recorder) event.OrphanScanCompletedPayload {
	t.Helper()
	evs := rec.ByType(event.EventOrphanScanCompleted)
	if len(evs) == 0 {
		t.Fatalf("EventOrphanScanCompleted: no events recorded")
	}
	p, ok := evs[len(evs)-1].Payload.(event.OrphanScanCompletedPayload)
	if !ok {
		t.Fatalf("EventOrphanScanCompleted: payload is %T, want OrphanScanCompletedPayload", evs[len(evs)-1].Payload)
	}
	return p
}

func reportCount(rec *eventfx.Recorder) int {
	return rec.Count(event.EventOrphanScanCompleted)
}

// manifestPathForID is the on-disk manifest path for a digest (manifest files
// are named by their digest).
func manifestPathForID(t *testing.T, digest domain.ManifestDigest) string {
	t.Helper()
	p, err := artifact.ManifestPath(digest)
	if err != nil {
		t.Fatalf("artifact.ManifestPath(%q): %v", digest, err)
	}
	return p
}

// fakeRef returns a blob-ref-shaped string with a recognisable hex tail. The
// suffix makes each ref distinct; total length is comfortably above the
// 4-char shard minimum.
func fakeRef(suffix byte) string {
	return strings.Repeat("ab", 16) + fmt.Sprintf("%02x", suffix)
}

// --- bootstrap orphan sweep --------------------------------------

// TestRecovery_SweepsOrphansAtInit: InitStore runs the orphan scan, removes
// every recognisable orphan (blob / manifest / staging) and publishes exactly
// one EventOrphanScanCompleted whose counters match what was swept. A fresh
// store removes nothing; the all-three case also carries a positive Duration.
func TestRecovery_SweepsOrphansAtInit(t *testing.T) {
	type want struct{ blobs, manifests, staging int }
	cases := []struct {
		name          string
		stage         func(t *testing.T, f *recoveryFixture) []string // staged paths that must be gone after
		want          want
		checkDuration bool
	}{
		{"fresh store, nothing staged", func(t *testing.T, f *recoveryFixture) []string { return nil }, want{0, 0, 0}, false},
		{"orphan blob", func(t *testing.T, f *recoveryFixture) []string {
			p := storekit.BlobPathForRef(t, fakeRef('a'))
			f.stageFile(t, p, "orphan blob content")
			return []string{p}
		}, want{1, 0, 0}, false},
		{"orphan manifest", func(t *testing.T, f *recoveryFixture) []string {
			p := manifestPathForID(t, domain.ManifestDigest(fakeRef('m')))
			f.stageFile(t, p, "{}")
			return []string{p}
		}, want{0, 1, 0}, false},
		{"staging leftover", func(t *testing.T, f *recoveryFixture) []string {
			p := ".staging/leftover-deadbeef"
			f.stageFile(t, p, "stale staging from a crashed prior write")
			return []string{p}
		}, want{0, 0, 1}, false},
		{"one of each", func(t *testing.T, f *recoveryFixture) []string {
			b := storekit.BlobPathForRef(t, fakeRef('1'))
			f.stageFile(t, b, "x")
			m := manifestPathForID(t, domain.ManifestDigest(fakeRef('2')))
			f.stageFile(t, m, "{}")
			st := ".staging/leftover-3"
			f.stageFile(t, st, "x")
			return []string{b, m, st}
		}, want{1, 1, 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			staged := tc.stage(t, f)

			_ = f.initStore(t)

			if got := reportCount(f.rec); got != 1 {
				t.Fatalf("EventOrphanScanCompleted: got %d events, want 1", got)
			}
			r := lastReport(t, f.rec)
			if r.BlobsRemoved != tc.want.blobs || r.ManifestsRemoved != tc.want.manifests || r.StagingRemoved != tc.want.staging {
				t.Errorf("removed counts: got {b:%d m:%d s:%d}, want {b:%d m:%d s:%d}; report=%+v",
					r.BlobsRemoved, r.ManifestsRemoved, r.StagingRemoved,
					tc.want.blobs, tc.want.manifests, tc.want.staging, r)
			}
			if r.NonFatalErrors != 0 {
				t.Errorf("NonFatalErrors: got %d, want 0", r.NonFatalErrors)
			}
			for _, p := range staged {
				if f.fileExists(t, p) {
					t.Errorf("orphan %q must be removed by recovery", p)
				}
			}
			if tc.checkDuration && r.Duration <= 0 {
				t.Errorf("payload.Duration: got %v, want > 0", r.Duration)
			}
		})
	}
}

// TestRecovery_LeavesUnparseableRef: a file at a Sharded-shaped path whose
// name is not "<algo>-<hex>" is left alone (we don't know what it is) and a
// non-fatal error is recorded — the scan does not crash or remove it.
func TestRecovery_LeavesUnparseableRef(t *testing.T) {
	f := newRecoveryFixture(t)

	junkPath := "blobs/aa/bb/not-a-blob-ref-just-some-text"
	f.stageFile(t, junkPath, "mystery file")

	_ = f.initStore(t)

	if !f.fileExists(t, junkPath) {
		t.Errorf("unparseable file %q must be left alone (recovery only removes recognisable refs)", junkPath)
	}
	r := lastReport(t, f.rec)
	if r.BlobsRemoved != 0 {
		t.Errorf("BlobsRemoved: got %d, want 0 (the file is not a recognisable ref)", r.BlobsRemoved)
	}
	if r.NonFatalErrors == 0 {
		t.Errorf("NonFatalErrors: got 0, want >=1 (parse failure was expected)")
	}
}

// TestRecovery_DoesNotTouchLiveArtifact: a real index-backed artifact survives
// a subsequent recovery pass — blob and manifest are left in place, Walk still
// lists it, Get still reads it.
func TestRecovery_DoesNotTouchLiveArtifact(t *testing.T) {
	f := newRecoveryFixture(t)
	s := f.initStore(t)

	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("real payload"))})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Observe only the second pass.
	f.rec.Clear()
	s2 := f.openStore(t)

	r := lastReport(t, f.rec)
	if r.BlobsRemoved != 0 || r.ManifestsRemoved != 0 || r.StagingRemoved != 0 {
		t.Errorf("live artifact run: removed counts must be 0, got %+v", r)
	}

	var seen []domain.ArtifactID
	if err := s2.Walk(context.Background(), func(m domain.Manifest) error {
		seen = append(seen, m.ArtifactID)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 1 || seen[0] != id {
		t.Errorf("Walk after recovery: got %v, want [%v]", seen, id)
	}

	rh, err := s2.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "real payload" {
		t.Errorf("payload mismatch: got %q, want %q", got, "real payload")
	}
}

// TestRecovery_OpenStore_RemovesOrphanInjectedAfterInit: orphans planted
// between sessions (a crash after Rename/Put but before IndexManifest) are
// swept on the next OpenStore, while the live artifact is untouched.
func TestRecovery_OpenStore_RemovesOrphanInjectedAfterInit(t *testing.T) {
	f := newRecoveryFixture(t)
	s := f.initStore(t)

	liveID, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("survivor"))})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	liveDigest := storekit.MustDigest(t, s, liveID)

	orphanBlob := storekit.BlobPathForRef(t, fakeRef('z'))
	f.stageFile(t, orphanBlob, "abandoned blob")
	orphanManifest := manifestPathForID(t, domain.ManifestDigest(fakeRef('y')))
	f.stageFile(t, orphanManifest, "{}")

	f.rec.Clear()
	s2 := f.openStore(t)

	if f.fileExists(t, orphanBlob) {
		t.Errorf("orphan blob %q must be removed", orphanBlob)
	}
	if f.fileExists(t, orphanManifest) {
		t.Errorf("orphan manifest %q must be removed", orphanManifest)
	}
	livePath := manifestPathForID(t, liveDigest)
	if !f.fileExists(t, livePath) {
		t.Errorf("live manifest %q must NOT be removed", livePath)
	}

	r := lastReport(t, f.rec)
	if r.BlobsRemoved != 1 {
		t.Errorf("BlobsRemoved: got %d, want 1", r.BlobsRemoved)
	}
	if r.ManifestsRemoved != 1 {
		t.Errorf("ManifestsRemoved: got %d, want 1", r.ManifestsRemoved)
	}

	rh, err := s2.Get(context.Background(), liveID)
	if err != nil {
		t.Fatalf("Get(live): %v", err)
	}
	got, _ := io.ReadAll(rh)
	rh.Close()
	if string(got) != "survivor" {
		t.Errorf("live payload mismatch: got %q, want %q", got, "survivor")
	}
}

// TestRecovery_NoPublisher_NoPanic: with no publisher wired, recovery still
// sweeps orphans — it just stays silent. Guards against a nil-publisher
// dereference in the report path.
func TestRecovery_NoPublisher_NoPanic(t *testing.T) {
	d := driverfx.LocalFS(t)

	orphan := storekit.BlobPathForRef(t, fakeRef('q'))
	if err := d.Put(context.Background(), orphan, strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, _, err := store.InitStore(context.Background(), d,
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore (no publisher): %v", err)
	}

	if _, err := d.Stat(context.Background(), orphan); err == nil {
		t.Errorf("orphan %q must be removed even without a publisher", orphan)
	}
}

// --- orphanscan package: transient index-error contracts ---------

// faultyIndex wraps a real StoreIndex and injects errors into the two methods
// recoverOrphans consults: Resolve (per-blob) and ManifestExistsByDigest
// (per-manifest). All other calls pass through. Used to exercise the
// "transient index error" branch: an index-infrastructure failure during a
// sweep must NOT remove the orphan (better to leave a possibly-orphan file
// than to mistake healthy data for orphan because of a transient SQLite
// hiccup). It wraps the index interface, so it has no driverfx equivalent.
type faultyIndex struct {
	index.StoreIndex
	resolveErr        error // if non-nil, Resolve returns this
	manifestExistsErr error // if non-nil, ManifestExistsByDigest returns this
}

func (f *faultyIndex) Resolve(ctx context.Context, ref string) (domain.PhysicalAddress, error) {
	if f.resolveErr != nil {
		return domain.PhysicalAddress{}, f.resolveErr
	}
	return f.StoreIndex.Resolve(ctx, ref)
}

func (f *faultyIndex) ManifestExistsByDigest(ctx context.Context, digest domain.ManifestDigest) (bool, error) {
	if f.manifestExistsErr != nil {
		return false, f.manifestExistsErr
	}
	return f.StoreIndex.ManifestExistsByDigest(ctx, digest)
}

// TestRecoverOrphans_TransientIndexError_Preserves: a transient error from the
// index (Resolve for blobs, ManifestExistsByDigest for manifests) must leave
// the corresponding file in place, remove nothing, and record the error.
func TestRecoverOrphans_TransientIndexError_Preserves(t *testing.T) {
	cases := []struct {
		name  string
		mkIdx func(base index.StoreIndex) *faultyIndex
		stage func(t *testing.T, drv driver.Driver) string // staged path that must survive
	}{
		{"resolve error preserves blob",
			func(b index.StoreIndex) *faultyIndex {
				return &faultyIndex{StoreIndex: b, resolveErr: errors.New("simulated SQLite busy")}
			},
			func(t *testing.T, drv driver.Driver) string {
				ref := strings.Repeat("ab", 16) + "cd"
				p := "blobs/" + ref[:4] + "/" + ref[4:8] + "/" + ref
				if err := drv.Put(context.Background(), p, strings.NewReader("orphan or not?")); err != nil {
					t.Fatalf("Put blob: %v", err)
				}
				return p
			}},
		{"manifestExists error preserves manifest",
			func(b index.StoreIndex) *faultyIndex {
				return &faultyIndex{StoreIndex: b, manifestExistsErr: errors.New("simulated index outage")}
			},
			func(t *testing.T, drv driver.Driver) string {
				id := strings.Repeat("ef", 16) + "00"
				p := "manifests/" + id[:4] + "/" + id[4:8] + "/" + id
				if err := drv.Put(context.Background(), p, strings.NewReader("{}")); err != nil {
					t.Fatalf("Put manifest: %v", err)
				}
				return p
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drv := driverfx.LocalFS(t)
			idx := tc.mkIdx(indexfx.Memory(t))
			path := tc.stage(t, drv)

			report, err := orphanscan.RecoverOrphans(context.Background(), drv, idx)
			if err != nil {
				t.Fatalf("RecoverOrphans: %v", err)
			}

			if _, err := drv.Stat(context.Background(), path); err != nil {
				t.Errorf("%q removed despite transient index error: %v", path, err)
			}
			if report.BlobsRemoved != 0 || report.ManifestsRemoved != 0 {
				t.Errorf("removed on transient error: blobs=%d manifests=%d, want 0/0", report.BlobsRemoved, report.ManifestsRemoved)
			}
			if len(report.Errors) == 0 {
				t.Errorf("Errors empty; want >=1 recording the transient failure")
			}
		})
	}
}

// TestRecoverOrphans_TransientResolveError_DoesNotAbortSweep: a Resolve error
// on blobs does not stop the sweep — staging is still cleaned, and one error
// is recorded per failed Resolve.
func TestRecoverOrphans_TransientResolveError_DoesNotAbortSweep(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := &faultyIndex{
		StoreIndex: indexfx.Memory(t),
		resolveErr: errors.New("transient"),
	}

	for i, suffix := range []string{"01", "02"} {
		ref := strings.Repeat("cd", 16) + suffix
		path := "blobs/" + ref[:4] + "/" + ref[4:8] + "/" + ref
		if err := drv.Put(context.Background(), path, strings.NewReader("blob")); err != nil {
			t.Fatalf("blob %d: %v", i, err)
		}
	}
	stagingPath := ".staging/leftover-from-crashed-put"
	if err := drv.Put(context.Background(), stagingPath, strings.NewReader("staging")); err != nil {
		t.Fatalf("staging: %v", err)
	}

	report, err := orphanscan.RecoverOrphans(context.Background(), drv, idx)
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

// TestRecoverOrphans_RemoveFails_OrphanStays: when the driver's Remove fails,
// the file stays and the injected error is recorded.
func TestRecoverOrphans_RemoveFails_OrphanStays(t *testing.T) {
	inner := driverfx.LocalFS(t)
	drv := driverfx.Faulty(t, inner,
		faulty.WithSeed(42),
		faulty.WithFailureRate(faulty.MethodRemove, 1.0),
	)

	stagingPath := ".staging/leftover-from-crash"
	if err := inner.Put(context.Background(), stagingPath, strings.NewReader("x")); err != nil {
		t.Fatalf("inner.Put: %v", err)
	}

	report, err := orphanscan.RecoverOrphans(context.Background(), drv, indexfx.Memory(t))
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

// TestRecoverOrphans_Default_RemovesUnknownBlob: the baseline — a blob the
// index does not know (Resolve returns NotFound on an empty index) is removed.
func TestRecoverOrphans_Default_RemovesUnknownBlob(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)

	ref := strings.Repeat("12", 16) + "ff"
	blobPath := "blobs/" + ref[:4] + "/" + ref[4:8] + "/" + ref
	if err := drv.Put(context.Background(), blobPath, strings.NewReader("orphan")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	report, err := orphanscan.RecoverOrphans(context.Background(), drv, idx)
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

// --- recovery kit: descriptor restore ----------------------------

// TestRestoreDescriptorFromRecoveryKit_RoundTrip bootstraps an encrypted Store
// (which emits a Recovery Kit), deletes both descriptor replicas to simulate
// catastrophic loss, then restores from the kit and asserts identity and
// crypto material round-trip exactly, including the L1 shadow replica.
func TestRestoreDescriptorFromRecoveryKit_RoundTrip(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)

	_, kit, err := store.InitStore(ctx, drv,
		store.WithHashRegistry(storefx.Hashes()),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithPassphrase(storefx.StaticPP("pw")),
	)
	if err != nil {
		t.Fatalf("InitStore (encrypted): %v", err)
	}
	if len(kit) == 0 {
		t.Fatal("InitStore returned an empty recovery kit for an encrypted store")
	}

	orig, err := descriptor.Read(ctx, drv)
	if err != nil {
		t.Fatalf("read original descriptor: %v", err)
	}

	// Simulate catastrophic descriptor loss: remove both replicas.
	root := drv.Root()
	for _, name := range []string{descriptor.Path, descriptor.BackupPath} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if _, err := descriptor.Read(ctx, drv); err == nil {
		t.Fatal("descriptor still readable after removing both replicas")
	}

	info, err := store.RestoreDescriptorFromRecoveryKit(ctx, drv, kit)
	if err != nil {
		t.Fatalf("RestoreDescriptorFromRecoveryKit: %v", err)
	}
	if !info.DescriptorWritten {
		t.Error("DescriptorWritten = false, want true")
	}
	if info.StoreID != orig.StoreID {
		t.Errorf("info.StoreID = %q, want %q", info.StoreID, orig.StoreID)
	}

	restored, err := descriptor.Read(ctx, drv)
	if err != nil {
		t.Fatalf("read restored descriptor (L0): %v", err)
	}
	if restored.StoreID != orig.StoreID {
		t.Errorf("StoreID = %q, want %q", restored.StoreID, orig.StoreID)
	}
	if !restored.DEKEncrypted {
		t.Error("restored descriptor not marked DEKEncrypted")
	}
	if !bytes.Equal(restored.DEK, orig.DEK) {
		t.Error("restored wrapped DEK differs from the original")
	}
	if restored.KDFParams == nil || orig.KDFParams == nil {
		t.Fatal("KDFParams missing on original or restored descriptor")
	}
	if restored.KDFParams.Algorithm != orig.KDFParams.Algorithm {
		t.Errorf("KDF algorithm = %q, want %q", restored.KDFParams.Algorithm, orig.KDFParams.Algorithm)
	}
	if !bytes.Equal(restored.KDFParams.Salt, orig.KDFParams.Salt) {
		t.Error("restored KDF salt differs from the original")
	}
	if restored.KDFParams.Time != orig.KDFParams.Time ||
		restored.KDFParams.Memory != orig.KDFParams.Memory ||
		restored.KDFParams.Threads != orig.KDFParams.Threads {
		t.Error("restored KDF cost parameters differ from the original")
	}

	rc, err := drv.Get(ctx, descriptor.BackupPath)
	if err != nil {
		t.Fatalf("L1 shadow descriptor not restored: %v", err)
	}
	_ = rc.Close()
}

// TestRestoreDescriptorFromRecoveryKit_Rejects: corrupted kit bytes yield the
// corrupted-kit sentinel; a nil driver is a rejected programming error.
func TestRestoreDescriptorFromRecoveryKit_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		drv     driver.Driver
		kit     []byte
		wantErr error // nil = any non-nil error is acceptable
	}{
		{"corrupted kit bytes", driverfx.LocalFS(t), []byte("not a recovery kit"), errs.ErrRecoveryKitCorrupted},
		{"nil driver", nil, []byte("x"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.RestoreDescriptorFromRecoveryKit(context.Background(), tc.drv, tc.kit)
			if tc.wantErr == nil {
				if err == nil {
					t.Fatal("got nil error, want non-nil")
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

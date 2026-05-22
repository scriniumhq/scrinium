package store_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/testutil/storefx"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// --- shared test fixtures and helpers ---------------------------

// recoveryFixture is the test rig for recovery scenarios. The
// Driver, Index, and Root are exposed separately so individual
// tests can stage on-disk preconditions BEFORE calling InitStore
// or OpenStore — the whole point of this file is exercising what
// the bootstrap scan does with files placed by a prior (crashed)
// process.
type recoveryFixture struct {
	drv  driver.Driver
	idx  index.StoreIndex
	root string
	bus  event.EventBus
	caps *capturedReports
}

// newRecoveryFixture builds a Driver + Index + EventBus combo and
// returns them WITHOUT calling InitStore. Tests stage their orphan
// preconditions, then invoke InitStore (or InitStore + OpenStore)
// and inspect both the on-disk effects and the EventBus payloads.
func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	d := driverfx.LocalFS(t)
	caps := newCapturedReports()
	bus := event.NewEventBus()
	bus.Subscribe(caps.handle)
	return &recoveryFixture{
		drv:  d,
		idx:  indexfx.Memory(t),
		root: d.Root(),
		bus:  bus,
		caps: caps,
	}
}

// initStore runs store.InitStore against the fixture and returns
// the resulting Store. EventOrphanScanCompleted has been published
// by the time this returns (the bus is synchronous).
func (f *recoveryFixture) initStore(t *testing.T) store.Store {
	t.Helper()
	s, _, err := store.InitStore(context.Background(), f.drv,
		store.WithStoreIndex(f.idx),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithPublisher(f.bus),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	return s
}

// openStore runs store.OpenStore on the same Driver+Index. Used for
// "crash-then-reopen" scenarios.
func (f *recoveryFixture) openStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.OpenStore(context.Background(), f.drv,
		store.WithStoreIndex(f.idx),
		store.WithHashRegistry(storefx.Hashes()),
		store.WithPublisher(f.bus),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s
}

// stageFile drops a file with the given content at the given
// root-relative path through the fixture's Driver. Used to plant
// synthetic orphan blobs / manifests / staging files BEFORE
// invoking InitStore/OpenStore.
func (f *recoveryFixture) stageFile(t *testing.T, path, content string) {
	t.Helper()
	if err := f.drv.Put(context.Background(), path, strings.NewReader(content)); err != nil {
		t.Fatalf("Driver.Put(%q): %v", path, err)
	}
}

// fileExists is a yes/no probe: "does the driver still see this
// path?" Used after recovery to assert removal.
func (f *recoveryFixture) fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := f.drv.Stat(context.Background(), path)
	return err == nil
}

// resetReports clears the captured event log so a follow-up
// InitStore/OpenStore call's events can be inspected in isolation.
func (f *recoveryFixture) resetReports() {
	f.caps.mu.Lock()
	f.caps.payloads = nil
	f.caps.mu.Unlock()
}

// capturedReports collects every EventOrphanScanCompleted payload
// in the order it was published.
type capturedReports struct {
	mu       sync.Mutex
	payloads []event.OrphanScanCompletedPayload
}

func newCapturedReports() *capturedReports {
	return &capturedReports{}
}

func (c *capturedReports) handle(e event.Event) {
	if e.Type != event.EventOrphanScanCompleted {
		return
	}
	p, ok := e.Payload.(event.OrphanScanCompletedPayload)
	if !ok {
		return // bad payload type — caught explicitly by a dedicated test
	}
	c.mu.Lock()
	c.payloads = append(c.payloads, p)
	c.mu.Unlock()
}

func (c *capturedReports) last(t *testing.T) event.OrphanScanCompletedPayload {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.payloads) == 0 {
		t.Fatalf("EventOrphanScanCompleted: no events seen")
	}
	return c.payloads[len(c.payloads)-1]
}

func (c *capturedReports) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}

// blobPathForRef computes the on-disk Sharded path for a blob ref
// using the same blobpath helper that core uses internally. Tests
// stage orphan files at the same paths the real Put pipeline would
// produce — the only difference is that nothing was inserted into
// the index.
func blobPathForRef(t *testing.T, ref string) string {
	t.Helper()
	p, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeRegular, ref)
	if err != nil {
		t.Fatalf("blobpath.Resolve(%q): %v", ref, err)
	}
	return p
}

// manifestPathForID is the manifest-side counterpart.
func manifestPathForID(t *testing.T, id domain.ArtifactID) string {
	t.Helper()
	p, err := blobpath.ManifestPath(id)
	if err != nil {
		t.Fatalf("blobpath.ManifestPath(%q): %v", id, err)
	}
	return p
}

// fakeRef returns a blob-ref-shaped string with a recognisable hex
// tail. The suffix lets each test produce a distinct ref; we hex-
// encode it so the ref is structurally valid regardless of whether
// the caller passes a hex literal or an arbitrary byte. Total
// length (32 hex chars from the "ab" filler + 2 from the suffix)
// is comfortably above shardOf's 4-char minimum.
func fakeRef(suffix byte) string {
	return "sha256-" + strings.Repeat("ab", 16) + fmt.Sprintf("%02x", suffix)
}

// --- 1. Fresh Store: no orphans -----------------------------------

func TestRecovery_FreshStore_NoOrphans(t *testing.T) {
	f := newRecoveryFixture(t)
	_ = f.initStore(t)

	if got := f.caps.count(); got != 1 {
		t.Fatalf("EventOrphanScanCompleted: got %d events, want 1", got)
	}
	r := f.caps.last(t)
	if r.StagingRemoved != 0 || r.BlobsRemoved != 0 || r.ManifestsRemoved != 0 {
		t.Errorf("fresh store: removed counts must be 0, got %+v", r)
	}
	if r.NonFatalErrors != 0 {
		t.Errorf("fresh store: NonFatalErrors must be 0, got %d", r.NonFatalErrors)
	}
}

// --- 2. Orphan blob removed at InitStore --------------------------

func TestRecovery_RemovesOrphanBlob_AtInit(t *testing.T) {
	f := newRecoveryFixture(t)

	ref := fakeRef('a')
	orphanPath := blobPathForRef(t, ref)
	f.stageFile(t, orphanPath, "orphan blob content")

	_ = f.initStore(t)

	if f.fileExists(t, orphanPath) {
		t.Errorf("orphan blob %q must be removed by recovery", orphanPath)
	}
	r := f.caps.last(t)
	if r.BlobsRemoved != 1 {
		t.Errorf("BlobsRemoved: got %d, want 1; report=%+v", r.BlobsRemoved, r)
	}
	if r.NonFatalErrors != 0 {
		t.Errorf("clean orphan blob: NonFatalErrors must be 0, got %d", r.NonFatalErrors)
	}
}

// --- 3. Orphan manifest removed at InitStore ----------------------

func TestRecovery_RemovesOrphanManifest_AtInit(t *testing.T) {
	f := newRecoveryFixture(t)

	id := domain.ArtifactID(fakeRef('m'))
	orphanPath := manifestPathForID(t, id)
	f.stageFile(t, orphanPath, "{}")

	_ = f.initStore(t)

	if f.fileExists(t, orphanPath) {
		t.Errorf("orphan manifest %q must be removed by recovery", orphanPath)
	}
	r := f.caps.last(t)
	if r.ManifestsRemoved != 1 {
		t.Errorf("ManifestsRemoved: got %d, want 1; report=%+v", r.ManifestsRemoved, r)
	}
}

// --- 4. Staging directory swept clean -----------------------------

func TestRecovery_RemovesStaging_AtInit(t *testing.T) {
	f := newRecoveryFixture(t)

	stagingPath := "system.state/staging/leftover-deadbeef"
	f.stageFile(t, stagingPath, "stale staging from a crashed prior write")

	_ = f.initStore(t)

	if f.fileExists(t, stagingPath) {
		t.Errorf("staging file %q must be removed by recovery", stagingPath)
	}
	r := f.caps.last(t)
	if r.StagingRemoved != 1 {
		t.Errorf("StagingRemoved: got %d, want 1; report=%+v", r.StagingRemoved, r)
	}
}

// --- 5. Live artifacts are not touched ----------------------------

func TestRecovery_DoesNotTouchLiveArtifact(t *testing.T) {
	f := newRecoveryFixture(t)
	s := f.initStore(t)

	// Put a real artifact through the regular pipeline. Both the
	// blob and the manifest become legitimate index-backed files;
	// a subsequent recovery pass must leave them alone.
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("real payload"))},
		domain.PutOptions{Namespace: "live"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Reset captured reports so we observe only the second pass.
	f.resetReports()

	// Re-open the same Store. Recovery runs again on every
	// transition into Unlocked.
	s2 := f.openStore(t)

	r := f.caps.last(t)
	if r.BlobsRemoved != 0 || r.ManifestsRemoved != 0 || r.StagingRemoved != 0 {
		t.Errorf("live artifact run: removed counts must be 0, got %+v", r)
	}

	// Walk: the artifact is still indexed.
	var seen []domain.ArtifactID
	if err := s2.Walk(context.Background(), "live", func(m domain.Manifest) error {
		seen = append(seen, m.ArtifactID)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 1 || seen[0] != id {
		t.Errorf("Walk after recovery: got %v, want [%v]", seen, id)
	}

	// Get: the artifact is still readable (the blob file survived).
	rh, err := s2.Get(context.Background(), id, domain.GetOptions{})
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

// --- 6. Crash-then-reopen: orphan introduced between sessions ----

func TestRecovery_OpenStore_RemovesOrphanInjectedAfterInit(t *testing.T) {
	f := newRecoveryFixture(t)
	s := f.initStore(t)

	// One real artifact so the Store is non-trivially populated.
	// Recovery must distinguish it from the planted orphans.
	liveID, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader([]byte("survivor"))},
		domain.PutOptions{Namespace: "live"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Plant an orphan blob at a path the index does not know about
	// — simulates a crash in Put after Driver.Rename(staging→final)
	// but before IndexManifest.
	orphanRef := fakeRef('z')
	orphanBlob := blobPathForRef(t, orphanRef)
	f.stageFile(t, orphanBlob, "abandoned blob")

	// Plant an orphan manifest similarly — crash after Driver.Put
	// on the manifest path but before IndexManifest.
	orphanID := domain.ArtifactID(fakeRef('y'))
	orphanManifest := manifestPathForID(t, orphanID)
	f.stageFile(t, orphanManifest, "{}")

	// Reset captured reports before re-opening.
	f.resetReports()

	s2 := f.openStore(t)

	// Orphans gone, live still there.
	if f.fileExists(t, orphanBlob) {
		t.Errorf("orphan blob %q must be removed", orphanBlob)
	}
	if f.fileExists(t, orphanManifest) {
		t.Errorf("orphan manifest %q must be removed", orphanManifest)
	}
	livePath := manifestPathForID(t, liveID)
	if !f.fileExists(t, livePath) {
		t.Errorf("live manifest %q must NOT be removed", livePath)
	}

	r := f.caps.last(t)
	if r.BlobsRemoved != 1 {
		t.Errorf("BlobsRemoved: got %d, want 1", r.BlobsRemoved)
	}
	if r.ManifestsRemoved != 1 {
		t.Errorf("ManifestsRemoved: got %d, want 1", r.ManifestsRemoved)
	}

	// Live artifact is still readable.
	rh, err := s2.Get(context.Background(), liveID, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get(live): %v", err)
	}
	got, _ := io.ReadAll(rh)
	rh.Close()
	if string(got) != "survivor" {
		t.Errorf("live payload mismatch: got %q, want %q", got, "survivor")
	}
}

// --- 7. Junk files under blobs/: don't crash, log non-fatal ------

func TestRecovery_LogsNonFatal_OnUnparseableRef(t *testing.T) {
	f := newRecoveryFixture(t)

	// File at a Sharded-shaped path but with a name that is NOT
	// "<algo>-<hex>". Recovery must NOT remove it (we don't know
	// what it is), but must record a non-fatal error and proceed.
	junkPath := "blobs/aa/bb/not-a-blob-ref-just-some-text"
	f.stageFile(t, junkPath, "mystery file")

	_ = f.initStore(t)

	if !f.fileExists(t, junkPath) {
		t.Errorf("unparseable file %q must be left alone (recovery only removes recognisable refs)", junkPath)
	}
	r := f.caps.last(t)
	if r.BlobsRemoved != 0 {
		t.Errorf("BlobsRemoved: got %d, want 0 (the file is not a recognisable ref)", r.BlobsRemoved)
	}
	if r.NonFatalErrors == 0 {
		t.Errorf("NonFatalErrors: got 0, want >=1 (parse failure was expected)")
	}
}

// --- 8. EventOrphanScanCompleted payload sanity --------------------

func TestRecovery_PublishesEvent_PayloadShape(t *testing.T) {
	f := newRecoveryFixture(t)

	// Stage one of each to populate every counter.
	f.stageFile(t, blobPathForRef(t, fakeRef('1')), "x")
	f.stageFile(t, manifestPathForID(t, domain.ArtifactID(fakeRef('2'))), "{}")
	f.stageFile(t, "system.state/staging/leftover-3", "x")

	_ = f.initStore(t)

	if got := f.caps.count(); got != 1 {
		t.Fatalf("expected exactly 1 EventOrphanScanCompleted, got %d", got)
	}
	r := f.caps.last(t)
	if r.BlobsRemoved != 1 {
		t.Errorf("payload.BlobsRemoved: got %d, want 1", r.BlobsRemoved)
	}
	if r.ManifestsRemoved != 1 {
		t.Errorf("payload.ManifestsRemoved: got %d, want 1", r.ManifestsRemoved)
	}
	if r.StagingRemoved != 1 {
		t.Errorf("payload.StagingRemoved: got %d, want 1", r.StagingRemoved)
	}
	if r.Duration <= 0 {
		t.Errorf("payload.Duration: got %v, want > 0", r.Duration)
	}
}

// --- 9. No publisher wired: recovery still runs, no panic --------

func TestRecovery_NoPublisher_NoPanic(t *testing.T) {
	d := driverfx.LocalFS(t)

	ref := fakeRef('q')
	orphan := blobPathForRef(t, ref)
	if err := d.Put(context.Background(), orphan, strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// InitStore without WithPublisher — recovery must still sweep
	// orphans, just silently. The point of the test is to catch a
	// nil-publisher dereference if one is ever introduced into
	// publishOrphanReport.
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

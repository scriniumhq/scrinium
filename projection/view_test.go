package projection_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/projection"
)

// --- fakeSource ---

// fakeSource is an in-memory ProjectionSource for unit tests. It
// holds a slice of manifests and a parallel map of payload bytes
// keyed by ArtifactID. Walk delivers manifests in insertion order;
// Get returns a ReadHandle over the bytes registered for the id.
//
// Errors can be injected per-call via walkErr / getErr.
type fakeSource struct {
	manifests []domain.Manifest
	payloads  map[domain.ArtifactID][]byte

	walkErr error
	getErr  error
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		payloads: make(map[domain.ArtifactID][]byte),
	}
}

func (f *fakeSource) add(m domain.Manifest, payload []byte) {
	f.manifests = append(f.manifests, m)
	if payload != nil {
		f.payloads[m.ArtifactID] = payload
	}
}

func (f *fakeSource) Walk(
	ctx context.Context,
	namespace string,
	cb func(domain.Manifest) error,
) error {
	if f.walkErr != nil {
		return f.walkErr
	}
	for _, m := range f.manifests {
		if namespace != "*" && m.Namespace != namespace {
			continue
		}
		if err := cb(m); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSource) Get(
	ctx context.Context,
	id domain.ArtifactID,
	opts domain.GetOptions,
) (core.ReadHandle, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	payload, ok := f.payloads[id]
	if !ok {
		return nil, errs.ErrArtifactNotFound
	}
	// Find manifest for this id.
	var manifest domain.Manifest
	for _, m := range f.manifests {
		if m.ArtifactID == id {
			manifest = m
			break
		}
	}
	return &fakeReadHandle{
		buf:      bytes.NewReader(payload),
		manifest: manifest,
	}, nil
}

// fakeReadHandle is a minimal core.ReadHandle backed by a bytes
// buffer. Random access is supported.
type fakeReadHandle struct {
	buf      *bytes.Reader
	manifest domain.Manifest
	closed   bool
}

func (h *fakeReadHandle) Read(p []byte) (int, error) {
	if h.closed {
		return 0, errors.New("fakeReadHandle: closed")
	}
	return h.buf.Read(p)
}

func (h *fakeReadHandle) ReadAt(p []byte, off int64) (int, error) {
	if h.closed {
		return 0, errors.New("fakeReadHandle: closed")
	}
	return h.buf.ReadAt(p, off)
}

func (h *fakeReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	return h.ReadAt(p, off)
}

func (h *fakeReadHandle) SupportsRandomAccess() bool { return true }

func (h *fakeReadHandle) Manifest() domain.Manifest { return h.manifest }

func (h *fakeReadHandle) Close() error {
	h.closed = true
	return nil
}

var _ core.ReadHandle = (*fakeReadHandle)(nil)

// --- helpers ---

func makeManifest(id, ns, sid string, size int64, createdAt time.Time) domain.Manifest {
	return domain.Manifest{
		ArtifactID:   domain.ArtifactID(id),
		Type:         domain.ManifestTypeBlob,
		Namespace:    ns,
		SessionID:    sid,
		CreatedAt:    createdAt,
		ContentHash:  domain.ContentHash(id),
		OriginalSize: size,
	}
}

// --- NewView ---

func TestNewView_Empty(t *testing.T) {
	src := newFakeSource()
	v, err := projection.NewView(context.Background(), src)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	defer v.Close()
	if v.Source != projection.SourceKindStore {
		t.Errorf("Source: got %q, want store", v.Source)
	}
	if v.Stats.TotalNodes != 0 {
		t.Errorf("TotalNodes: got %d, want 0", v.Stats.TotalNodes)
	}
}

func TestNewView_NilSource(t *testing.T) {
	_, err := projection.NewView(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil source")
	}
}

func TestNewView_SourceWalkError(t *testing.T) {
	src := newFakeSource()
	src.walkErr = errors.New("source kaboom")
	_, err := projection.NewView(context.Background(), src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errs.ErrSourceUnavailable) {
		t.Errorf("expected ErrSourceUnavailable, got %v", err)
	}
}

// --- Backfill ---

func TestNewView_PopulatesByArtifact(t *testing.T) {
	src := newFakeSource()
	now := time.Now().UTC()
	src.add(makeManifest("sha256-aabbccdd", "files", "sess1", 100, now), []byte("hello"))
	src.add(makeManifest("sha256-eeffgghh", "files", "sess1", 200, now), []byte("world"))

	v, err := projection.NewView(context.Background(), src)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	defer v.Close()

	if got := v.Stats.TotalNodes; got != 2 {
		t.Errorf("TotalNodes: got %d, want 2", got)
	}
	if got := v.Stats.TotalBytes; got != 300 {
		t.Errorf("TotalBytes: got %d, want 300", got)
	}
}

// --- Get ---

func TestGet_File(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "files", "sess1", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	// by-artifact path: aa/bb/sha256-aabbccdd
	node, err := v.GetByArtifact("aa/bb/sha256-aabbccdd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if node.FS.IsDir {
		t.Errorf("expected file, got dir")
	}
	if node.Artifact == nil {
		t.Fatal("expected Artifact to be populated")
	}
	if node.Artifact.ArtifactID != domain.ArtifactID("sha256-aabbccdd") {
		t.Errorf("ArtifactID: got %q", node.Artifact.ArtifactID)
	}
}

func TestGet_VirtualDirectory(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "files", "sess1", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	// "aa" is a virtual directory created by sharding.
	node, err := v.GetByArtifact("aa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !node.FS.IsDir {
		t.Errorf("expected dir, got file")
	}
	if node.Artifact != nil {
		t.Errorf("virtual dir should not have Artifact")
	}
}

func TestGet_Root(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "files", "sess1", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	node, err := v.GetByArtifact("")
	if err != nil {
		t.Fatalf("Get root: %v", err)
	}
	if !node.FS.IsDir {
		t.Errorf("root should be dir")
	}
}

func TestGet_NotFound(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	_, err := v.GetByArtifact("nonexistent")
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestGet_Closed(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src)
	v.Close()

	_, err := v.GetByArtifact("")
	if !errors.Is(err, errs.ErrViewClosed) {
		t.Errorf("expected ErrViewClosed, got %v", err)
	}
}

// --- List ---

func TestList_RootListsShards(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)
	src.add(makeManifest("sha256-ccddeeff", "f", "s", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	var names []string
	for n, err := range v.ListByArtifact("") {
		if err != nil {
			t.Fatalf("iter error: %v", err)
		}
		names = append(names, n.FS.Name)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 children at root, got %d (%v)", len(names), names)
	}
	if names[0] != "aa" || names[1] != "cc" {
		t.Errorf("expected [aa, cc] sorted, got %v", names)
	}
}

func TestList_FileReturnsErrNotADirectory(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	for _, err := range v.ListByArtifact("aa/bb/sha256-aabbccdd") {
		if !errors.Is(err, errs.ErrNotADirectory) {
			t.Errorf("expected ErrNotADirectory, got %v", err)
		}
		return
	}
	t.Fatal("expected at least one yield with error")
}

func TestList_NonexistentReturnsErrPathNotFound(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	for _, err := range v.ListByArtifact("nope/path") {
		if !errors.Is(err, errs.ErrPathNotFound) {
			t.Errorf("expected ErrPathNotFound, got %v", err)
		}
		return
	}
	t.Fatal("expected at least one yield with error")
}

// --- Walk ---

func TestWalk_AllNodes(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)
	src.add(makeManifest("sha256-aaccddee", "f", "s", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	var paths []string
	for n, err := range v.WalkByArtifact("") {
		if err != nil {
			t.Fatalf("iter error: %v", err)
		}
		paths = append(paths, n.FS.Path)
	}
	// Expected: root "", "aa", "aa/bb", "aa/bb/sha256-aabbccdd",
	// "aa/cc", "aa/cc/sha256-aaccddee".
	if len(paths) != 6 {
		t.Errorf("expected 6 paths, got %d: %v", len(paths), paths)
	}
}

func TestWalk_StopEarly(t *testing.T) {
	src := newFakeSource()
	for i := 0; i < 10; i++ {
		id := makeShortID(i)
		src.add(makeManifest(id, "f", "s", 100, time.Now().UTC()), nil)
	}
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	count := 0
	for _, err := range v.WalkByArtifact("") {
		if err != nil {
			t.Fatalf("iter error: %v", err)
		}
		count++
		if count >= 3 {
			break
		}
	}
	if count != 3 {
		t.Errorf("expected exactly 3, got %d", count)
	}
}

// --- Open ---

func TestOpen_File(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 5, time.Now().UTC()), []byte("hello"))
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	rh, err := v.OpenByArtifact(context.Background(), "aa/bb/sha256-aabbccdd", domain.GetOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestOpen_Directory(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	_, err := v.OpenByArtifact(context.Background(), "aa", domain.GetOptions{})
	if !errors.Is(err, errs.ErrIsADirectory) {
		t.Errorf("expected ErrIsADirectory, got %v", err)
	}
}

func TestOpen_NotFound(t *testing.T) {
	src := newFakeSource()
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	_, err := v.OpenByArtifact(context.Background(), "nope", domain.GetOptions{})
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestOpen_SourceArtifactNotFound(t *testing.T) {
	// View has the path (was indexed), but source returns
	// ErrArtifactNotFound — race with concurrent deletion.
	// Mapping should yield ErrPathNotFound on the projection side.
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	// Replace getErr on the source to simulate the race.
	src.getErr = errs.ErrArtifactNotFound

	_, err := v.OpenByArtifact(context.Background(), "aa/bb/sha256-aabbccdd", domain.GetOptions{})
	if !errors.Is(err, errs.ErrPathNotFound) {
		t.Errorf("expected ErrPathNotFound, got %v", err)
	}
}

func TestOpen_SourceLocked(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "f", "s", 100, time.Now().UTC()), nil)
	v, _ := projection.NewView(context.Background(), src)
	defer v.Close()

	src.getErr = errs.ErrLocked

	_, err := v.OpenByArtifact(context.Background(), "aa/bb/sha256-aabbccdd", domain.GetOptions{})
	if !errors.Is(err, errs.ErrArtifactUnreadable) {
		t.Errorf("expected ErrArtifactUnreadable, got %v", err)
	}
	if !errors.Is(err, errs.ErrLocked) {
		t.Errorf("expected ErrLocked to be wrapped; got %v", err)
	}
}

// --- Filter ---

func TestNewView_FilterByNamespace(t *testing.T) {
	src := newFakeSource()
	src.add(makeManifest("sha256-aabbccdd", "photos", "s", 100, time.Now().UTC()), nil)
	src.add(makeManifest("sha256-eeffaabb", "docs", "s", 100, time.Now().UTC()), nil)

	v, err := projection.NewView(
		context.Background(),
		src,
		projection.WithFilter(projection.ViewFilter{Namespace: "photos"}),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	defer v.Close()

	if got := v.Stats.TotalNodes; got != 1 {
		t.Errorf("TotalNodes: got %d, want 1", got)
	}
}

// --- Helper ---

// makeShortID creates a fake but unique ArtifactID for tests
// that need many distinct IDs.
func makeShortID(i int) string {
	const hex = "0123456789abcdef"
	const algo = "sha256-"
	b := make([]byte, 64)
	for j := range b {
		b[j] = hex[(i+j)%16]
	}
	return algo + string(b)
}

package projectionfx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/projection/fsmeta"
	"github.com/rkurbatov/scrinium/testutil/manifestfx"
)

// --- FakeSource ---

// FakeSource is an in-memory ProjectionSource for unit tests.
// Manifests are registered through Add together with their
// payload bytes; Walk iterates in insertion order; Get serves
// payloads as FakeReadHandle.
//
// FakeSource is not safe for concurrent Add/Walk — tests build it
// up before running the unit under test. It is safe for
// concurrent Get calls once construction is done.
//
// Errors can be injected per-method via SetWalkErr and SetGetErr.
type FakeSource struct {
	mu        sync.RWMutex
	manifests []domain.Manifest
	payloads  map[domain.ArtifactID][]byte

	walkErr   error
	getErr    error
	putErr    error
	deleteErr error

	// putCounter generates unique synthetic ArtifactIDs for Put.
	putCounter uint64
}

// New returns an empty FakeSource ready to use as
// projection.ProjectionSource.
func New() *FakeSource {
	return &FakeSource{
		payloads: make(map[domain.ArtifactID][]byte),
	}
}

// Add registers a manifest and (optionally) its payload bytes.
// payload may be nil — callers that only test Walk-level paths
// don't need to wire payloads.
func (f *FakeSource) Add(m domain.Manifest, payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests = append(f.manifests, m)
	if payload != nil {
		f.payloads[m.ArtifactID] = payload
	}
}

// SetWalkErr installs an error to be returned by every subsequent
// Walk call. Pass nil to clear.
func (f *FakeSource) SetWalkErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.walkErr = err
}

// SetGetErr installs an error to be returned by every subsequent
// Get call. Pass nil to clear.
func (f *FakeSource) SetGetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getErr = err
}

// Walk iterates the registered manifests. namespace="*" matches
// every namespace; otherwise only manifests whose Namespace
// field equals the argument.
func (f *FakeSource) Walk(
	ctx context.Context,
	namespace string,
	cb func(domain.Manifest) error,
) error {
	f.mu.RLock()
	walkErr := f.walkErr
	manifests := append([]domain.Manifest(nil), f.manifests...)
	f.mu.RUnlock()

	if walkErr != nil {
		return walkErr
	}
	for _, m := range manifests {
		if namespace != "*" && m.Namespace != namespace {
			continue
		}
		if err := cb(m); err != nil {
			return err
		}
	}
	return nil
}

// Get returns a ReadHandle over the payload registered for id, or
// errs.ErrArtifactNotFound when none is registered.
func (f *FakeSource) Get(
	ctx context.Context,
	id domain.ArtifactID,
	opts domain.GetOptions,
) (core.ReadHandle, error) {
	f.mu.RLock()
	getErr := f.getErr
	payload, hasPayload := f.payloads[id]
	var manifest domain.Manifest
	for _, m := range f.manifests {
		if m.ArtifactID == id {
			manifest = m
			break
		}
	}
	f.mu.RUnlock()

	if getErr != nil {
		return nil, getErr
	}
	if !hasPayload {
		return nil, errs.ErrArtifactNotFound
	}
	return NewReadHandle(payload, WithManifest(manifest)), nil
}

// Put consumes the artifact payload, generates a synthetic
// ArtifactID, and stores both the manifest and the bytes. The
// returned ID embeds an incrementing counter so multiple Puts in
// the same test produce distinct IDs without colliding.
//
// The synthetic ContentHash is derived from the counter as well,
// keeping every artifact's hash distinct (the dedup-on-content
// behaviour of the real Store is irrelevant here — tests that
// need it construct manifests by hand and use Add).
func (f *FakeSource) Put(
	ctx context.Context,
	a domain.Artifact,
	opts domain.PutOptions,
) (domain.ArtifactID, error) {
	f.mu.Lock()
	if f.putErr != nil {
		err := f.putErr
		f.mu.Unlock()
		return "", err
	}
	f.putCounter++
	counter := f.putCounter
	f.mu.Unlock()

	// Drain the payload into bytes — tests can re-serve it via Get.
	var payload []byte
	if a.Payload != nil {
		buf, err := io.ReadAll(a.Payload)
		if err != nil {
			return "", fmt.Errorf("projectionfx.FakeSource.Put: read payload: %w", err)
		}
		payload = buf
	}

	id := domain.ArtifactID(fmt.Sprintf("sha256-%064x", counter))
	hash := domain.ContentHash(fmt.Sprintf("sha256-%064x", counter+0x10000))

	m := domain.Manifest{
		ArtifactID:   id,
		Type:         domain.ManifestTypeBlob,
		Namespace:    opts.Namespace,
		SessionID:    opts.SessionID,
		CreatedAt:    time.Now().UTC(),
		ContentHash:  hash,
		BlobRef:      domain.BlobRef(hash),
		OriginalSize: int64(len(payload)),
		Metadata:     a.Metadata,
	}

	f.mu.Lock()
	f.manifests = append(f.manifests, m)
	f.payloads[id] = payload
	f.mu.Unlock()

	return id, nil
}

// Delete drops the artifact (manifest + payload) from the store.
// Idempotent: deleting an unknown id is a no-op (matches
// core.Store semantics).
func (f *FakeSource) Delete(ctx context.Context, id domain.ArtifactID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i, m := range f.manifests {
		if m.ArtifactID == id {
			f.manifests = append(f.manifests[:i], f.manifests[i+1:]...)
			break
		}
	}
	delete(f.payloads, id)
	return nil
}

// SetPutErr installs an error returned by every subsequent Put
// call. nil clears.
func (f *FakeSource) SetPutErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putErr = err
}

// SetDeleteErr installs an error returned by every subsequent
// Delete call. nil clears.
func (f *FakeSource) SetDeleteErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteErr = err
}

// Manifests returns a snapshot of every manifest currently held by
// the source. Useful for assertions that count or scan recently
// stored artifacts.
func (f *FakeSource) Manifests() []domain.Manifest {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]domain.Manifest, len(f.manifests))
	copy(out, f.manifests)
	return out
}

// --- FakeReadHandle ---

// FakeReadHandle is a core.ReadHandle backed by bytes. Designed
// for tests; supports random access by default but can be
// configured stream-only via options.
type FakeReadHandle struct {
	buf      *bytes.Reader
	manifest domain.Manifest
	random   bool
	closed   bool
}

// ReadHandleOption is the option type for NewReadHandle.
type ReadHandleOption func(*readHandleOptions)

type readHandleOptions struct {
	manifest domain.Manifest
	random   bool
}

// WithManifest associates a manifest with the handle. The
// manifest is returned by Manifest() and is used when the
// transport layer needs ContentHash/Type/etc. for the artifact
// being read.
func WithManifest(m domain.Manifest) ReadHandleOption {
	return func(o *readHandleOptions) { o.manifest = m }
}

// WithStreamOnly forces SupportsRandomAccess to return false and
// makes ReadAt fail. Use to test FSOps/transport paths that have
// to fall back to sequential reading.
func WithStreamOnly() ReadHandleOption {
	return func(o *readHandleOptions) { o.random = false }
}

// NewReadHandle returns a FakeReadHandle over payload. By default
// random access is enabled; use WithStreamOnly to disable.
func NewReadHandle(payload []byte, opts ...ReadHandleOption) *FakeReadHandle {
	o := readHandleOptions{
		random: true,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return &FakeReadHandle{
		buf:      bytes.NewReader(payload),
		manifest: o.manifest,
		random:   o.random,
	}
}

// errReadHandleClosed is returned by every method on a closed
// handle. Defined once so tests that want to assert against it
// can use errors.Is.
var errReadHandleClosed = errors.New("projectionfx: ReadHandle closed")

// errReadHandleNoRandomAccess is returned by ReadAt/ReadAtCtx
// when the handle was constructed with WithStreamOnly. Tests
// that exercise the stream-only fallback can match against this.
var errReadHandleNoRandomAccess = errors.New("projectionfx: ReadHandle does not support random access")

func (h *FakeReadHandle) Read(p []byte) (int, error) {
	if h.closed {
		return 0, errReadHandleClosed
	}
	return h.buf.Read(p)
}

func (h *FakeReadHandle) ReadAt(p []byte, off int64) (int, error) {
	if h.closed {
		return 0, errReadHandleClosed
	}
	if !h.random {
		return 0, errReadHandleNoRandomAccess
	}
	return h.buf.ReadAt(p, off)
}

func (h *FakeReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.ReadAt(p, off)
}

func (h *FakeReadHandle) SupportsRandomAccess() bool { return h.random }

func (h *FakeReadHandle) Manifest() domain.Manifest { return h.manifest }

func (h *FakeReadHandle) Close() error {
	h.closed = true
	return nil
}

// Compile-time guard.
var _ core.ReadHandle = (*FakeReadHandle)(nil)

// --- Manifest builders ---

// ManifestWithFsmetaPath returns a manifest produced by
// manifestfx.Blob, augmented with fsmeta-encoded metadata for
// the given virtual path. Use this whenever a test needs an
// artifact that should land in by-path/.
//
// id is the ArtifactID; blobRef defaults to "sha256-bbbb..."
// (manifestfx convention) — tests that need a specific blobRef
// pass it explicitly through manifestfx.Blob and then call
// AddFsmetaPath on the result.
func ManifestWithFsmetaPath(id, path string) domain.Manifest {
	m := manifestfx.Blob(id, "sha256-"+repeat('b', 64))
	if err := AddFsmetaPath(&m, path); err != nil {
		panic("projectionfx.ManifestWithFsmetaPath: " + err.Error())
	}
	return m
}

// AddFsmetaPath sets m.Metadata to an fsmeta.FileSystem-encoded
// payload with the given path. Returns the underlying
// fsmeta.Encode error so callers writing negative-path tests
// (deliberately invalid paths) can assert against it.
func AddFsmetaPath(m *domain.Manifest, path string) error {
	raw, err := fsmeta.Encode(fsmeta.FileSystem{Path: path})
	if err != nil {
		return err
	}
	m.Metadata = raw
	return nil
}

// repeat is a tiny helper to avoid pulling in strings.Repeat for
// a single use. Mirrors the synthetic-hash pattern of manifestfx.
func repeat(b byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return string(out)
}

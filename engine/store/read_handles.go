package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
)

// read_handles.go — the coreapi.ReadHandle implementations returned
// by Get and SystemStore.Get. Three concrete handles, collected here
// rather than scattered across get.go and a standalone file:
//   - inlineReadHandle    — bytes already in the manifest (inline blobs,
//                           system artifacts). Trivial buffer reader.
//   - targetReadHandle    — lazily opens the physical blob via the
//                           store Get-path; supports range reads.
//   - verifyingReadHandle — decorator that rehashes plaintext on read
//                           and reports a mismatch through publish.
// All three stay in package store: target and verifying are bound to
// *store internals (buildGetReader, publish), so they cannot leave the
// package without dragging the Get-path with them.

// --- inlineReadHandle: bytes live in the manifest itself ---

type inlineReadHandle struct {
	manifest domain.Manifest
	reader   *bytes.Reader
}

func (h *inlineReadHandle) Read(p []byte) (int, error) {
	return h.reader.Read(p)
}

func (h *inlineReadHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.reader.ReadAt(p, off)
}

func (h *inlineReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.reader.ReadAt(p, off)
}

func (h *inlineReadHandle) SupportsRandomAccess() bool {
	return true
}

func (h *inlineReadHandle) Close() error {
	// In-memory; nothing to release. Idempotent.
	return nil
}

func (h *inlineReadHandle) Manifest() domain.Manifest {
	return h.manifest
}

// --- targetReadHandle: blob is a separate file under /blobs/ ---
//
// The handle defers opening the blob until the first Read. ReadAt
// always opens a fresh range descriptor through Driver.ReadAt and
// closes it before returning — that is the simplest way to layer
// random access over a streaming Driver.Get without sharing state
// with the linear Read cursor.

type targetReadHandle struct {
	manifest domain.Manifest
	drv      driver.Driver
	blobPath string
	// ctx captured at Get-time; used for non-Ctx Read/ReadAt that
	// have no ambient context per the io.Reader/io.ReaderAt
	// contracts. ReadAtCtx ignores this field and uses its own.
	ctx   context.Context
	store *store

	mu     sync.Mutex
	rc     io.ReadCloser // lazily opened on first Read
	closed bool
}

func (h *targetReadHandle) Read(p []byte) (int, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, os.ErrClosed
	}
	if h.rc == nil {
		// Open the on-disk blob.
		raw, err := h.drv.Get(h.ctx, h.blobPath)
		if err != nil {
			h.mu.Unlock()
			if errors.Is(err, os.ErrNotExist) {
				return 0, errs.ErrCorruptedBlob
			}
			return 0, err
		}
		// Compose the inverse Pipeline (no-op when empty).
		decoded, err := h.store.buildGetReader(h.manifest.Pipeline, raw)
		if err != nil {
			h.mu.Unlock()
			return 0, err
		}
		h.rc = decoded
	}
	rc := h.rc
	h.mu.Unlock()
	return rc.Read(p)
}

func (h *targetReadHandle) ReadAt(p []byte, off int64) (int, error) {
	if !h.SupportsRandomAccess() {
		return 0, errs.ErrRandomAccessNotSupported
	}
	return h.readAt(h.ctx, p, off)
}

func (h *targetReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	if !h.SupportsRandomAccess() {
		return 0, errs.ErrRandomAccessNotSupported
	}
	return h.readAt(ctx, p, off)
}

func (h *targetReadHandle) readAt(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, os.ErrClosed
	}
	h.mu.Unlock()

	rc, err := h.drv.ReadAt(ctx, h.blobPath, off, int64(len(p)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, errs.ErrCorruptedBlob
		}
		return 0, err
	}
	defer rc.Close()
	n, readErr := io.ReadFull(rc, p)
	// io.ReaderAt contract: when n < len(p), err must be non-nil
	// and is typically io.EOF for short reads. ReadFull returns
	// ErrUnexpectedEOF in that case; convert to EOF for the
	// caller.
	if errors.Is(readErr, io.ErrUnexpectedEOF) {
		readErr = io.EOF
	}
	return n, readErr
}

func (h *targetReadHandle) SupportsRandomAccess() bool {
	// A non-empty Pipeline transforms the on-disk bytes; ReadAt
	// would have to replay the inverse chain from the start to
	// reach an arbitrary offset, defeating its purpose. We
	// therefore advertise random access only when the manifest
	// stores the original bytes verbatim.
	return len(h.manifest.Pipeline) == 0
}

func (h *targetReadHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil // idempotent
	}
	h.closed = true
	if h.rc == nil {
		return nil
	}
	err := h.rc.Close()
	h.rc = nil
	return err
}

func (h *targetReadHandle) Manifest() domain.Manifest {
	return h.manifest
}

// Compile-time interface conformance.
var (
	_ coreapi.ReadHandle = (*inlineReadHandle)(nil)
)

// verifyingReadHandle wraps a ReadHandle and rehashes the
// plaintext bytes as they stream through Read. On a clean EOF
// (every byte consumed) the running hash is compared against
// manifest.ContentHash; a mismatch surfaces as
// errs.ErrCorruptedBlob and emits EventScrubFailed.
//
// ReadAt is intentionally not verified: random access reads only
// a slice, so a streaming hash makes no sense — the absence of a
// full pass over the bytes is detectable at the interface level
// (ReadAt is exposed on the inner handle, this wrapper does not
// touch ContentHash on the random-access path). Pipeline-bearing
// handles already report SupportsRandomAccess() == false, so the
// usual VerifyOnRead targets are exactly the streaming path. For
// plain Inline/Target blobs that DO support ReadAt, the policy
// trades full-stream verification for a per-call check the
// caller must opt into explicitly (Verify).
//
// The wrapper is created only when shouldVerifyOnRead returns
// true; otherwise Get returns the inner handle unchanged.
type verifyingReadHandle struct {
	inner coreapi.ReadHandle
	store *store

	algo   string
	want   []byte
	hasher hash.Hash
	read   int64
	limit  int64 // declared OriginalSize; -1 if unknown

	mu       sync.Mutex
	closed   bool
	eofSeen  bool
	mismatch bool
}

// newVerifyingReadHandle constructs the wrapper. It parses
// manifest.ContentHash up front so a malformed hash identifier is
// surfaced at Get time, not at the first Read — callers prefer
// failures to happen at the construction boundary.
//
// Returns the inner handle unchanged (and a nil error) when the
// manifest has no ContentHash to verify against; that branch
// keeps the wrapper layered cleanly over old or unusual
// manifests without forcing a special case at every call site.
func newVerifyingReadHandle(inner coreapi.ReadHandle, s *store) (coreapi.ReadHandle, error) {
	m := inner.Manifest()
	if m.ContentHash == "" {
		return inner, nil
	}
	algo, want, hasher, err := s.parseContentHash(m.ContentHash)
	if err != nil {
		_ = inner.Close()
		return nil, fmt.Errorf("core.Get: %w", err)
	}
	limit := int64(-1)
	if m.OriginalSize > 0 {
		limit = m.OriginalSize
	}
	return &verifyingReadHandle{
		inner:  inner,
		store:  s,
		algo:   algo,
		want:   want,
		hasher: hasher,
		limit:  limit,
	}, nil
}

func (h *verifyingReadHandle) Read(p []byte) (int, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, os.ErrClosed
	}
	h.mu.Unlock()

	n, err := h.inner.Read(p)
	if n > 0 {
		// Hash whatever bytes the inner reader delivered. We do
		// it outside the lock so concurrent Close — which is a
		// caller bug per io.ReadCloser, but we must not deadlock
		// — sees a consistent view of the closed flag.
		_, _ = h.hasher.Write(p[:n])
		h.mu.Lock()
		h.read += int64(n)
		h.mu.Unlock()
	}
	if errors.Is(err, io.EOF) {
		if vErr := h.finalize(); vErr != nil {
			// Return the verification error in place of EOF —
			// the caller's io.ReadAll/io.Copy loop will surface
			// it. Callers that distinguish errors via errors.Is
			// see ErrCorruptedBlob; the underlying EOF is no
			// longer interesting.
			return n, vErr
		}
	}
	return n, err
}

// finalize compares the running hash with manifest.ContentHash.
// Called once on the first EOF; subsequent invocations are a
// no-op. Emits EventScrubFailed on divergence.
func (h *verifyingReadHandle) finalize() error {
	h.mu.Lock()
	if h.eofSeen {
		h.mu.Unlock()
		return nil
	}
	h.eofSeen = true
	read := h.read
	limit := h.limit
	h.mu.Unlock()

	// Length cross-check: a short stream that hashed to the
	// expected value would still be corrupt. OriginalSize is
	// authoritative when populated.
	if limit >= 0 && read != limit {
		err := fmt.Errorf("%w: read %d bytes, manifest declares %d",
			errs.ErrCorruptedBlob, read, limit)
		h.markMismatch(err)
		return err
	}

	got := h.hasher.Sum(nil)
	if !bytes.Equal(got, h.want) {
		err := fmt.Errorf("%w: ContentHash mismatch (algo=%s)",
			errs.ErrCorruptedBlob, h.algo)
		h.markMismatch(err)
		return err
	}
	return nil
}

func (h *verifyingReadHandle) markMismatch(err error) {
	h.mu.Lock()
	h.mismatch = true
	h.mu.Unlock()
	h.store.publish(event.EventScrubFailed, event.ScrubFailedPayload{
		ArtifactID: h.inner.Manifest().ArtifactID,
		Err:        err,
	})
}

// ReadAt is forwarded to the inner handle. Random-access reads
// are not covered by streaming verification — see the type-level
// note. Callers that need full-stream verification should use
// Read or call Verify directly.
func (h *verifyingReadHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.inner.ReadAt(p, off)
}

func (h *verifyingReadHandle) ReadAtCtx(ctx context.Context, p []byte, off int64) (int, error) {
	return h.inner.ReadAtCtx(ctx, p, off)
}

func (h *verifyingReadHandle) SupportsRandomAccess() bool {
	return h.inner.SupportsRandomAccess()
}

func (h *verifyingReadHandle) Manifest() domain.Manifest {
	return h.inner.Manifest()
}

func (h *verifyingReadHandle) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	return h.inner.Close()
}

// Compile-time interface conformance.
var _ coreapi.ReadHandle = (*verifyingReadHandle)(nil)

package ejector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// Eject materialises the whole artifact (fire-and-forget).
func (a *ejectorAgent) Eject(ctx context.Context, id domain.ArtifactID) (string, error) {
	p, _, err := a.ejectWhole(ctx, id, false)
	return p, err
}

// Hold materialises the whole artifact and returns a holder.
func (a *ejectorAgent) Hold(ctx context.Context, id domain.ArtifactID) (EjectHandle, error) {
	p, ch, err := a.ejectWhole(ctx, id, true)
	if err != nil {
		return nil, err
	}
	return &ejectHandle{a: a, ch: ch, path: p}, nil
}

func (a *ejectorAgent) ejectWhole(ctx context.Context, id domain.ArtifactID, hold bool) (string, string, error) {
	rh, err := a.st.Get(ctx, id)
	if err != nil {
		a.emitFailed(id, err)
		return "", "", err
	}
	man := rh.Manifest()
	ch := string(man.ContentHash)
	size := man.OriginalSize

	// Reuse without reading the blob (ContentHash is known from the manifest).
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		rh.Close()
		return "", "", errs.ErrEjectorClosed
	}
	if e := a.byHash[ch]; e != nil && a.reuseOK(e) {
		if hold {
			e.holders++
		}
		e.lastAccess = time.Now()
		p := e.path
		a.mu.Unlock()
		rh.Close()
		return p, ch, nil
	}
	a.mu.Unlock()

	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	case <-ctx.Done():
		rh.Close()
		return "", "", ctx.Err()
	}

	final := filepath.Join(a.cfg.TempDir, encodeName(ch))
	vh, err := a.atomicWrite(final, func(w io.Writer) error {
		_, cerr := io.Copy(w, rh)
		return cerr
	})
	rh.Close()
	if err != nil {
		a.emitFailed(id, err)
		return "", "", err
	}

	now := time.Now()
	a.mu.Lock()
	e := a.byHash[ch]
	if e == nil {
		e = &entry{path: final, verifyHash: vh, size: size, extractedAt: now}
		a.byHash[ch] = e
	}
	if hold {
		e.holders++
	}
	e.lastAccess = now
	a.mu.Unlock()

	a.sizeCapEvict()
	a.emitEjected(id, ch, final, "copy", 0, size)
	return final, ch, nil
}

// EjectFragment materialises [start, end) (fire-and-forget).
func (a *ejectorAgent) EjectFragment(ctx context.Context, id domain.ArtifactID, start, end int64) (string, error) {
	if start < 0 || end <= start {
		return "", fmt.Errorf("%w: [%d,%d)", errs.ErrInvalidRange, start, end)
	}
	if a.cfg.MaxFragmentBytes > 0 && end-start > a.cfg.MaxFragmentBytes {
		return "", fmt.Errorf("%w: %d bytes", errs.ErrFragmentTooLarge, end-start)
	}

	rh, err := a.st.Get(ctx, id)
	if err != nil {
		a.emitFailed(id, err)
		return "", err
	}
	if size := rh.Manifest().OriginalSize; size > 0 && end > size {
		rh.Close()
		return "", fmt.Errorf("%w: end %d > size %d", errs.ErrInvalidRange, end, size)
	}

	rk := reqKey{id: id, start: start, end: end}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		rh.Close()
		return "", errs.ErrEjectorClosed
	}
	if ch, ok := a.byReq[rk]; ok {
		if e := a.byHash[ch]; e != nil && a.reuseOK(e) {
			e.lastAccess = time.Now()
			p := e.path
			a.mu.Unlock()
			rh.Close()
			return p, nil
		}
	}
	a.mu.Unlock()

	if start > 0 && !rh.SupportsRandomAccess() {
		rh.Close()
		return "", errs.ErrRandomAccessNotSupported
	}

	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	case <-ctx.Done():
		rh.Close()
		return "", ctx.Err()
	}

	ch, final, vh, err := a.writeFragment(ctx, rh, start, end)
	rh.Close()
	if err != nil {
		a.emitFailed(id, err)
		return "", err
	}

	now := time.Now()
	a.mu.Lock()
	a.byReq[rk] = ch
	e := a.byHash[ch]
	if e == nil {
		e = &entry{path: final, verifyHash: vh, size: end - start, extractedAt: now}
		a.byHash[ch] = e
	}
	e.lastAccess = now
	a.mu.Unlock()

	a.sizeCapEvict()
	a.emitEjected(id, ch, final, "copy", start, end-start)
	return final, nil
}

// writeFragment reads [start, end), hashing as it goes, and renames the
// result to TempDir/<encoded fragment hash>. Existing identical fragment
// files are reused (deduplicated).
func (a *ejectorAgent) writeFragment(ctx context.Context, rh domain.ReadHandle, start, end int64) (ch, final, vh string, err error) {
	suffix, err := randHex()
	if err != nil {
		return "", "", "", fmt.Errorf("ejector.Ejector: temp name: %w", err)
	}
	tmp := filepath.Join(a.cfg.TempDir, ".tmp-"+suffix)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", "", "", mapDiskErr(err)
	}
	h := sha256.New()
	mw := io.MultiWriter(f, h)

	if start == 0 {
		if _, cerr := io.CopyN(mw, rh, end); cerr != nil && cerr != io.EOF {
			f.Close()
			os.Remove(tmp)
			return "", "", "", mapDiskErr(cerr)
		}
	} else {
		if cerr := copyRangeAt(ctx, mw, rh, start, end); cerr != nil {
			f.Close()
			os.Remove(tmp)
			return "", "", "", mapDiskErr(cerr)
		}
	}
	if cerr := f.Sync(); cerr != nil {
		f.Close()
		os.Remove(tmp)
		return "", "", "", mapDiskErr(cerr)
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(tmp)
		return "", "", "", mapDiskErr(cerr)
	}

	vh = hex.EncodeToString(h.Sum(nil))
	ch = "sha256-" + vh
	final = filepath.Join(a.cfg.TempDir, encodeName(ch))
	if _, serr := os.Stat(final); serr == nil {
		os.Remove(tmp) // identical fragment already present
		return ch, final, vh, nil
	}
	if rerr := os.Rename(tmp, final); rerr != nil {
		os.Remove(tmp)
		return "", "", "", rerr
	}
	return ch, final, vh, nil
}

// copyRangeAt copies [start, end) from a random-access ReadHandle.
func copyRangeAt(ctx context.Context, w io.Writer, rh domain.ReadHandle, start, end int64) error {
	buf := make([]byte, 256*1024)
	off := start
	for off < end {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if rem := end - off; rem < want {
			want = rem
		}
		n, err := rh.ReadAtCtx(ctx, buf[:want], off)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			off += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	return nil
}

// atomicWrite writes via a private temp file, fsync, and rename into
// final. Returns the sha256 hex of the bytes written.
func (a *ejectorAgent) atomicWrite(final string, fill func(w io.Writer) error) (string, error) {
	suffix, err := randHex()
	if err != nil {
		return "", fmt.Errorf("ejector.Ejector: temp name: %w", err)
	}
	tmp := filepath.Join(a.cfg.TempDir, ".tmp-"+suffix)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", mapDiskErr(err)
	}
	h := sha256.New()
	if cerr := fill(io.MultiWriter(f, h)); cerr != nil {
		f.Close()
		os.Remove(tmp)
		return "", mapDiskErr(cerr)
	}
	if cerr := f.Sync(); cerr != nil {
		f.Close()
		os.Remove(tmp)
		return "", mapDiskErr(cerr)
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(tmp)
		return "", mapDiskErr(cerr)
	}
	if rerr := os.Rename(tmp, final); rerr != nil {
		os.Remove(tmp)
		return "", rerr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// reuseOK reports whether an existing entry may be reused. With
// VerifyOnReuse, the file is re-hashed and dropped on mismatch.
func (a *ejectorAgent) reuseOK(e *entry) bool {
	if !a.cfg.VerifyOnReuse {
		return true
	}
	f, err := os.Open(e.path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == e.verifyHash
}

// release decrements a holder count.
func (a *ejectorAgent) release(ch string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.byHash[ch]; e != nil && e.holders > 0 {
		e.holders--
		e.lastAccess = time.Now()
	}
}

// ejectHandle is a holder on a whole-artifact materialisation.
type ejectHandle struct {
	a    *ejectorAgent
	ch   string
	path string
	once sync.Once
}

func (h *ejectHandle) Path() string { return h.path }

func (h *ejectHandle) Release() error {
	h.once.Do(func() { h.a.release(h.ch) })
	return nil
}

// encodeName maps a content hash to a filesystem-safe name. The mapping
// (base64 '+'/'/' -> '-'/'_') is bijective, so distinct hashes never
// collide; hex hashes are unaffected.
func encodeName(ch string) string {
	return strings.NewReplacer("/", "_", "+", "-").Replace(ch)
}

func randHex() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func mapDiskErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return fmt.Errorf("ejector.Ejector: %w", errs.ErrEjectorTempDirFull)
	}
	return fmt.Errorf("ejector.Ejector: %w", err)
}

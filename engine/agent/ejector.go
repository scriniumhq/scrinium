package agent

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
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// --- Sync Agent (Reserved, D-05; scheduled for M4/S3) ---

// SyncAgent replicates artifacts between a Target and a Backup Store.
// Reserved: interface fixed for API stability, implementation deferred
// to M4/S3 pending the reconciliation-mechanism decision.
type SyncAgent interface {
	Agent
	Trigger(ctx context.Context, artifactID domain.ArtifactID) error
}

// --- Ejector Agent (ADR-70) ---
//
// The Ejector materialises artifacts onto the host filesystem as plain
// POSIX files and returns a path, for consumers that need a file path
// rather than an io.Reader (a player, ImageMagick, an external binary).
// Consumers that can take a stream use Store.Get directly — the Ejector
// is path-only (ADR-70 §"Get vs Eject"). Files are named by ContentHash
// (the store's plaintext dedup key), so identical content materialises
// once regardless of which artifact id resolves to it. The materialised
// set is a private, reproducible, plaintext scratch — never the sole
// copy of anything (ADR-67), so it is reclaimable at will.
//
// The Ejector is a resident agent (ADR-68): it holds a volatile table
// of materialisations in memory; the assembler builds it once and holds
// it for the Store's lifetime, closing it via cascade.

// EjectorConfig configures the Ejector. TempDir is required.
type EjectorConfig struct {
	// TempDir is the private per-instance scratch directory (created
	// 0700, swept on open, not shared between instances).
	TempDir string

	// MaxConcurrent bounds simultaneous materialisations (default 4).
	MaxConcurrent int

	// KeepAliveIdle reclaims a materialisation with zero holders this
	// long after its last access. 0 = never (only Close / open-sweep /
	// size-cap reclaim).
	KeepAliveIdle time.Duration

	// MaxLifetime is the absolute cap on a materialisation's age even
	// with holders > 0. 0 = never.
	MaxLifetime time.Duration

	// MaxScratchBytes caps total scratch size; on overflow, zero-holder
	// entries are evicted oldest-first. 0 = unlimited.
	MaxScratchBytes int64

	// MaxFragmentBytes rejects EjectFragment requests larger than this.
	// 0 = unlimited.
	MaxFragmentBytes int64

	// VerifyOnReuse re-hashes an existing file before reusing it; on
	// mismatch it is re-materialised. Default false (trust isolation).
	VerifyOnReuse bool
}

func (c *EjectorConfig) applyDefaults() {
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 4
	}
	// Timers default to 0 = never; the scheduler/assembler may set them.
}

// Ejector materialises artifacts as host files (ADR-70).
type Ejector interface {
	Agent

	// Eject materialises the whole artifact and returns its path.
	// Fire-and-forget: takes no holder, lives under KeepAliveIdle.
	// Idempotent and content-addressed: identical content returns the
	// same path. A returned path is a complete, ready-to-read file.
	Eject(ctx context.Context, id domain.ArtifactID) (string, error)

	// EjectFragment materialises the byte range [start, end) and returns
	// its path (named by the fragment's own hash). Fire-and-forget.
	// start==0 always works; start>0 requires random access on the
	// ReadHandle, else ErrRandomAccessNotSupported.
	EjectFragment(ctx context.Context, id domain.ArtifactID, start, end int64) (string, error)

	// Hold materialises the whole artifact and returns a holder; while
	// held, the file is not reclaimed (except MaxLifetime / Close /
	// open-sweep). For consumers that need "do not reclaim while I work".
	Hold(ctx context.Context, id domain.ArtifactID) (EjectHandle, error)

	// Close stops the Ejector and removes all scratch, ignoring holders.
	// Idempotent.
	Close() error
}

// EjectHandle is a holder on a materialisation. Release decrements the
// holder count; idempotent.
type EjectHandle interface {
	Path() string
	Release() error
}

type entry struct {
	path        string
	verifyHash  string // sha256 hex of the materialised bytes
	size        int64
	holders     int
	extractedAt time.Time
	lastAccess  time.Time
}

type reqKey struct {
	id    domain.ArtifactID
	start int64
	end   int64
}

type ejectorAgent struct {
	baseState

	st      store.Store
	bus     event.Publisher
	storeID string
	cfg     EjectorConfig

	sem chan struct{} // bounds concurrent materialisations

	mu     sync.Mutex
	byHash map[string]*entry // contentHash -> entry (reclamation key)
	byReq  map[reqKey]string // (id,start,end) -> contentHash (skip-read memo)
	closed bool
}

var _ Ejector = (*ejectorAgent)(nil)

// NewEjector constructs an Ejector. TempDir is required; it is created
// and swept (crash leftovers removed) on open.
func NewEjector(st store.Store, bus event.Publisher, storeID string, cfg EjectorConfig, opts ...AgentOption) (Ejector, error) {
	if st == nil {
		return nil, fmt.Errorf("agent.NewEjector: nil store")
	}
	if cfg.TempDir == "" {
		return nil, fmt.Errorf("agent.NewEjector: TempDir is required")
	}
	cfg.applyDefaults()
	if err := os.MkdirAll(cfg.TempDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent.NewEjector: temp dir: %w", err)
	}
	a := &ejectorAgent{
		st:      st,
		bus:     bus,
		storeID: storeID,
		cfg:     cfg,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		byHash:  make(map[string]*entry),
		byReq:   make(map[reqKey]string),
	}
	a.log = resolveAgentLogger(opts)
	a.sweepOnOpen()
	return a, nil
}

// ejectorFactory builds the Ejector from the registry (ADR-51).
type ejectorFactory struct{}

func (ejectorFactory) Name() string { return "ejector" }

func (ejectorFactory) Build(st store.Store, cfg any, deps AgentDeps) (Agent, error) {
	c, _ := cfg.(EjectorConfig)
	return NewEjector(st, deps.Publisher, deps.StoreID, c, WithAgentLogger(deps.Logger))
}

func init() { Register(ejectorFactory{}) }

func (a *ejectorAgent) AgentType() string { return "ejector" }

func (a *ejectorAgent) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return errs.ErrEjectorClosed
	}
	return nil
}

// Run performs one reclamation sweep (idle + lifetime). The scheduler
// (ADR-69) drives periodicity; the Ejector has no background goroutine.
func (a *ejectorAgent) Run(ctx context.Context) (*domain.AgentResult, error) {
	a.setState(StateRunning, nil)
	if err := ctx.Err(); err != nil {
		a.setState(StateFaulted, err)
		return &domain.AgentResult{AgentType: "ejector", StoreID: a.storeID, CompletedAt: time.Now(), Partial: true}, err
	}
	counts := a.reclaim(time.Now())
	a.emitCleanup(counts)
	var total int64
	for _, n := range counts {
		total += int64(n)
	}
	a.setState(StateIdle, nil)
	return &domain.AgentResult{
		AgentType:   "ejector",
		StoreID:     a.storeID,
		CompletedAt: time.Now(),
		Stats:       map[string]int64{"evicted": total},
	}, nil
}

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
		return "", "", "", fmt.Errorf("agent.Ejector: temp name: %w", err)
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
		return "", fmt.Errorf("agent.Ejector: temp name: %w", err)
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

// Close stops the Ejector and removes all scratch, ignoring holders.
func (a *ejectorAgent) Close() error {
	a.mu.Lock()
	a.closed = true
	paths := make([]string, 0, len(a.byHash))
	for _, e := range a.byHash {
		paths = append(paths, e.path)
	}
	a.byHash = make(map[string]*entry)
	a.byReq = make(map[reqKey]string)
	a.mu.Unlock()
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			a.logger().Debug("ejector: scratch remove on close failed", "path", p, "err", err)
		}
	}
	return nil
}

// reclaim removes idle (zero-holder) and over-lifetime entries; returns
// eviction counts per reason. File removal happens outside the lock.
func (a *ejectorAgent) reclaim(now time.Time) map[string]int {
	type victim struct{ ch, path, reason string }
	a.mu.Lock()
	var vs []victim
	for ch, e := range a.byHash {
		switch {
		case a.cfg.MaxLifetime > 0 && now.Sub(e.extractedAt) >= a.cfg.MaxLifetime:
			vs = append(vs, victim{ch, e.path, "lifetime"})
		case e.holders == 0 && a.cfg.KeepAliveIdle > 0 && now.Sub(e.lastAccess) >= a.cfg.KeepAliveIdle:
			vs = append(vs, victim{ch, e.path, "idle"})
		}
	}
	for _, v := range vs {
		delete(a.byHash, v.ch)
		a.dropReqLocked(v.ch)
	}
	a.mu.Unlock()
	counts := map[string]int{}
	for _, v := range vs {
		if err := os.Remove(v.path); err != nil {
			a.logger().Debug("ejector: scratch remove on reclaim failed", "path", v.path, "err", err)
		}
		counts[v.reason]++
	}
	return counts
}

// sizeCapEvict enforces MaxScratchBytes by evicting zero-holder entries
// oldest-first. Held entries are never evicted (backpressure).
func (a *ejectorAgent) sizeCapEvict() {
	if a.cfg.MaxScratchBytes <= 0 {
		return
	}
	a.mu.Lock()
	var total int64
	for _, e := range a.byHash {
		total += e.size
	}
	var removed []string
	for total > a.cfg.MaxScratchBytes {
		var victimCh string
		var victim *entry
		for ch, e := range a.byHash {
			if e.holders != 0 {
				continue
			}
			if victim == nil || e.lastAccess.Before(victim.lastAccess) {
				victim, victimCh = e, ch
			}
		}
		if victim == nil {
			break // everything held
		}
		total -= victim.size
		removed = append(removed, victim.path)
		delete(a.byHash, victimCh)
		a.dropReqLocked(victimCh)
	}
	a.mu.Unlock()
	for _, p := range removed {
		if err := os.Remove(p); err != nil {
			a.logger().Debug("ejector: scratch remove under size pressure failed", "path", p, "err", err)
		}
	}
	if n := len(removed); n > 0 {
		a.emitCleanup(map[string]int{"pressure": n})
	}
}

// dropReqLocked removes byReq memo entries pointing at ch. Caller holds mu.
func (a *ejectorAgent) dropReqLocked(ch string) {
	for rk, v := range a.byReq {
		if v == ch {
			delete(a.byReq, rk)
		}
	}
}

// sweepOnOpen removes any files left in TempDir by a previous instance
// (safe: scratch is reproducible).
func (a *ejectorAgent) sweepOnOpen() {
	ents, err := os.ReadDir(a.cfg.TempDir)
	if err != nil {
		return
	}
	for _, de := range ents {
		p := filepath.Join(a.cfg.TempDir, de.Name())
		if err := os.Remove(p); err != nil {
			a.logger().Debug("ejector: open-sweep remove failed", "path", p, "err", err)
		}
	}
}

func (a *ejectorAgent) emitEjected(id domain.ArtifactID, ch, path, method string, start, length int64) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(event.Event{Type: event.EventArtifactEjected, Payload: event.ArtifactEjectedPayload{
		AgentType: "ejector", StoreID: a.storeID, ArtifactID: id,
		ContentHash: ch, Path: path, Method: method, Start: start, Length: length,
	}})
}

func (a *ejectorAgent) emitFailed(id domain.ArtifactID, err error) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(event.Event{Type: event.EventEjectFailed, Payload: event.EjectFailedPayload{
		AgentType: "ejector", StoreID: a.storeID, ArtifactID: id, Err: err,
	}})
}

func (a *ejectorAgent) emitCleanup(counts map[string]int) {
	if a.bus == nil {
		return
	}
	for reason, n := range counts {
		if n == 0 {
			continue
		}
		a.bus.Publish(event.Event{Type: event.EventEjectorCleanup, Payload: event.EjectorCleanupPayload{
			AgentType: "ejector", StoreID: a.storeID, Evicted: int64(n), Reason: reason,
		}})
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
		return fmt.Errorf("agent.Ejector: %w", errs.ErrEjectorTempDirFull)
	}
	return fmt.Errorf("agent.Ejector: %w", err)
}

package lease

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/named"
	"scrinium.dev/engine/internal/timefmt"
	"scrinium.dev/errs"
)

// The lease is the keep=0 exclusive cell in its policy form (ADR-100/101):
// a single fixed slot (named.CellPath(Name)) acquired by exclusive-create and
// overwritten in place, with TTL / heartbeat / nonce-takeover layered on
// top. It is the sole consumer of the exclusive-cell semantics that also
// needs a lifetime, so it lives here, beside the cell primitive it wraps.
//
// Bootstrap note: a lease is acquired before the store's configurable
// HashRegistry is wired (location.lock at OpenStore). The lease therefore
// does NOT use that registry — it builds and verifies its cell with a
// compiled-in sha256 (leaseHashes), so it has no bootstrap dependency.
// The cell is nonetheless a standard inline manifest: any store can read
// it later through SystemStore.Get/Walk because sha256 is universal.

// Record is the in-memory lease body. On disk it is the inline payload
// of the cell's manifest, one line of JSON (§11.2); the
// AcquiredAt/ExpiresAt timestamps are encoded through timefmt — the
// canonical RFC3339-second-UTC format the durable layer (index/sqlite,
// engine/artifact) shares — so a lease written by one subsystem parses
// byte-identically in another. Custom JSON methods keep callers working
// with time.Time while the wire form stays canonical.
type Record struct {
	HostID     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
	AgentType  string
	Nonce      string
	// StoreID is the identity of the store this lease belongs to (the
	// descriptor StoreID, ADR-104). Empty for location.lock, which is
	// acquired before the descriptor is read, and for leases written by
	// pre-store_id code; an empty StoreID is treated as "unknown", never
	// as foreign (see Acquire).
	StoreID string
}

// recordWire is the on-disk JSON shape: timestamps as canonical
// timefmt strings rather than Go's default RFC3339Nano-with-offset.
type recordWire struct {
	HostID     string `json:"host_id"`
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt  string `json:"expires_at"`
	AgentType  string `json:"agent_type"`
	Nonce      string `json:"nonce"`
	StoreID    string `json:"store_id,omitempty"`
}

// MarshalJSON encodes the record with timefmt-formatted timestamps.
func (r Record) MarshalJSON() ([]byte, error) {
	return json.Marshal(recordWire{
		HostID:     r.HostID,
		AcquiredAt: timefmt.Format(r.AcquiredAt),
		ExpiresAt:  timefmt.Format(r.ExpiresAt),
		AgentType:  r.AgentType,
		Nonce:      r.Nonce,
		StoreID:    r.StoreID,
	})
}

// UnmarshalJSON parses a record written by MarshalJSON, reading the
// timestamps through timefmt.Parse.
func (r *Record) UnmarshalJSON(b []byte) error {
	var w recordWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	acquired, err := timefmt.Parse(w.AcquiredAt)
	if err != nil {
		return fmt.Errorf("lease: parse acquired_at: %w", err)
	}
	expires, err := timefmt.Parse(w.ExpiresAt)
	if err != nil {
		return fmt.Errorf("lease: parse expires_at: %w", err)
	}
	r.HostID = w.HostID
	r.AcquiredAt = acquired
	r.ExpiresAt = expires
	r.AgentType = w.AgentType
	r.Nonce = w.Nonce
	r.StoreID = w.StoreID
	return nil
}

// expired reports whether the lease is past its TTL relative to now.
func (r Record) expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Lease is a held lease. It is not safe for concurrent use by multiple
// goroutines except that Renew and Release may run on a heartbeat
// goroutine while the holder reads the immutable identity fields.
type Lease struct {
	drv       driver.Driver
	name      string
	hostID    string
	agentType string
	ttl       time.Duration
	nonce     string
	storeID   string
}

// Config configures Acquire.
type Config struct {
	// Name is the system-artifact name of the lease cell, e.g.
	// "store.agent.gc.lease". The cell lives at named.CellPath(Name).
	// Required and must be a valid name (see ValidateName).
	Name string

	// HostID is the UUID v4 the process generated in-memory at
	// OpenStore. Shared across every lease the process holds, so a
	// takeover can report a meaningful previous owner. Required.
	HostID string

	// AgentType is the semantic owner tag ("RebuildIndex", "Migrate",
	// "GC"). Empty for location.lock.
	AgentType string

	// TTL is the hold time; ExpiresAt = AcquiredAt + TTL. Required
	// (> 0).
	TTL time.Duration

	// StoreID is the identity of the store this lease belongs to (the
	// descriptor StoreID, ADR-104). Optional: location.lock is acquired
	// before the descriptor is read and passes "". When set, Acquire
	// treats a live lease that explicitly carries a DIFFERENT StoreID as
	// absent and reclaims the slot — a foreign store's lease that leaked
	// through a shared/copied Location is not a conflict to wait out. A
	// lease with no recorded StoreID (location.lock, or pre-store_id code)
	// is "unknown", never foreign, so it keeps the normal live-lease
	// protection.
	StoreID string

	// Force overrides a live lease held by a DIFFERENT host of the SAME
	// store — an explicit operator escape hatch. It does NOT affect the
	// foreign-store reclaim (always reclaimed) or the expired-lease
	// takeover (always taken); it has no effect on an empty or expired
	// slot. Dangerous: the override writes a fresh Nonce, so the displaced
	// holder aborts on its next Renew (ErrLeaseLost) — callers MUST log a
	// forced acquisition loudly and emit an event.
	Force bool
}

// LeaseHeldError reports that Acquire refused because a live lease is
// held by a different host of the same store and Force was not set. It
// carries the current holder's identity for an actionable message and
// unwraps to errs.ErrLeaseHeld, so existing errors.Is(err,
// errs.ErrLeaseHeld) checks — including the transient-retry classifier —
// keep working.
type LeaseHeldError struct {
	HostID    string
	AgentType string
	ExpiresAt time.Time
}

func (e *LeaseHeldError) Error() string {
	agent := e.AgentType
	if agent == "" {
		agent = "(none)"
	}
	return fmt.Sprintf("scrinium: lease held by host %s (agent %s) until %s",
		e.HostID, agent, timefmt.Format(e.ExpiresAt))
}

func (e *LeaseHeldError) Unwrap() error { return errs.ErrLeaseHeld }

// Acquire takes the lease per §11.2 (ADR-104 reaction table). It succeeds
// when the slot is empty (exclusive create wins), the current lease has
// expired (takeover), the current lease explicitly belongs to a different
// store (foreign-store reclaim), or Force overrides a live same-store
// lease held by another host. A live lease held by a different host of the
// same (or unknown) store, without Force, is refused with a
// *LeaseHeldError that names the holder and unwraps to errs.ErrLeaseHeld.
//
// prev is the record we displaced when it belonged to a different host (a
// stale takeover, a foreign-store reclaim, or a forced override), so the
// caller can emit the corresponding event / loud log; prev is nil when the
// slot was empty or the lease was already ours.
func Acquire(ctx context.Context, drv driver.Driver, cfg Config) (l *Lease, prev *Record, err error) {
	if cfg.Name == "" || cfg.HostID == "" || cfg.TTL <= 0 {
		return nil, nil, fmt.Errorf("lease.Acquire: Name, HostID and TTL>0 are required")
	}
	if err := named.ValidateName(cfg.Name); err != nil {
		return nil, nil, fmt.Errorf("lease.Acquire: %w", err)
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, nil, fmt.Errorf("lease.Acquire: nonce: %w", err)
	}
	l = &Lease{
		drv:       drv,
		name:      cfg.Name,
		hostID:    cfg.HostID,
		agentType: cfg.AgentType,
		ttl:       cfg.TTL,
		nonce:     nonce,
		storeID:   cfg.StoreID,
	}

	current, err := l.read(ctx)
	switch {
	case errors.Is(err, errs.ErrAlreadyExists) || isNotFound(err):
		// Empty slot — try an exclusive create. If we lose the race
		// the create fails with ErrAlreadyExists; surface it as held.
		if werr := l.write(ctx, true); werr != nil {
			if errors.Is(werr, errs.ErrAlreadyExists) {
				return nil, nil, errs.ErrLeaseHeld
			}
			return nil, nil, werr
		}
		return l, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("lease.Acquire: read: %w", err)
	}

	now := time.Now()
	// Foreign store: a lease explicitly owned by a different store leaked
	// through a shared/copied Location — not our lease, not a conflict, so
	// reclaim the slot (fall through to the takeover write). The same
	// classifier backs the system-artifact read check (ADR-104); an empty
	// StoreID on either side is "unknown", never foreign, so location.lock
	// and pre-store_id leases keep the normal protection below.
	foreign := domain.ClassifyStoreOwnership(current.StoreID, cfg.StoreID) == domain.StoreOwnershipForeign
	if !foreign && !current.expired(now) && current.HostID != cfg.HostID && !cfg.Force {
		// Live lease, same (or unknown) store, different host, no override:
		// refuse with an actionable error naming the current holder.
		return nil, nil, &LeaseHeldError{
			HostID:    current.HostID,
			AgentType: current.AgentType,
			ExpiresAt: current.ExpiresAt,
		}
	}

	// Reclaim the slot — expired, ours (re-acquire), foreign-store, or a
	// forced override: overwrite, then read back and verify our nonce won,
	// which settles concurrent takeover without a coordinator.
	if werr := l.write(ctx, false); werr != nil {
		return nil, nil, fmt.Errorf("lease.Acquire: takeover write: %w", werr)
	}
	after, rerr := l.read(ctx)
	if rerr != nil {
		return nil, nil, fmt.Errorf("lease.Acquire: takeover verify: %w", rerr)
	}
	if after.Nonce != l.nonce {
		return nil, nil, errs.ErrLeaseHeld // another taker won the race
	}
	// prev is the record we displaced when it belonged to a different host
	// — a stale-lease takeover, a foreign-store reclaim, or a forced
	// override — so the caller can emit the corresponding event / loud log.
	// nil when the slot was empty or the lease was already ours.
	if current.HostID != cfg.HostID {
		c := current
		prev = &c
	}
	return l, prev, nil
}

// Renew extends the lease (§11.2): rewrite with a fresh ExpiresAt,
// keeping HostID and Nonce. It returns ErrLeaseLost when the holder
// finds the lease was taken over (HostID or Nonce no longer ours) —
// the operation must then abort. Renew is called at TTL/2.
func (l *Lease) Renew(ctx context.Context) error {
	current, err := l.read(ctx)
	if err != nil {
		if isNotFound(err) {
			return errs.ErrLeaseLost
		}
		return fmt.Errorf("lease.Renew: read: %w", err)
	}
	if current.HostID != l.hostID || current.Nonce != l.nonce {
		return errs.ErrLeaseLost
	}
	if err := l.write(ctx, false); err != nil {
		return fmt.Errorf("lease.Renew: write: %w", err)
	}
	return nil
}

// Release deletes the lease so a successor can acquire immediately,
// without waiting out the TTL (§11.2). Releasing a lease we no longer
// own (taken over) is a no-op: we do not delete a foreign holder's
// lease. A missing lease is also a no-op.
func (l *Lease) Release(ctx context.Context) error {
	current, err := l.read(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("lease.Release: read: %w", err)
	}
	if current.HostID != l.hostID || current.Nonce != l.nonce {
		return nil // taken over — not ours to delete
	}
	if err := named.RemoveCell(ctx, l.drv, l.name); err != nil {
		return fmt.Errorf("lease.Release: remove: %w", err)
	}
	return nil
}

// Heartbeat renews the lease at TTL/2 until ctx is cancelled. It
// returns ctx.Err() on cancellation, or ErrLeaseLost (wrapped) the
// first time a Renew finds the lease taken over — the caller treats
// that as a signal to abort the protected operation. Run it in its own
// goroutine for the lifetime of the hold.
func (l *Lease) Heartbeat(ctx context.Context) error {
	interval := l.ttl / 2
	if interval <= 0 {
		interval = l.ttl
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := l.Renew(ctx); err != nil {
				return fmt.Errorf("lease.Heartbeat: %w", err)
			}
		}
	}
}

// --- internals ---

func (l *Lease) record() Record {
	now := time.Now()
	return Record{
		HostID:     l.hostID,
		AcquiredAt: now,
		ExpiresAt:  now.Add(l.ttl),
		AgentType:  l.agentType,
		Nonce:      l.nonce,
		StoreID:    l.storeID,
	}
}

// write builds the lease record as a standard inline manifest (sha256
// computed through the compiled-in leaseHashes, no store registry) and
// writes it to the cell. exclusive=true is the empty-slot acquire;
// exclusive=false is renew/takeover overwrite.
func (l *Lease) write(ctx context.Context, exclusive bool) error {
	recordJSON, err := json.Marshal(l.record())
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	fileBytes, _, err := named.BuildInlineManifest(recordJSON, leaseHashAlgo, leaseHashes)
	if err != nil {
		return fmt.Errorf("build lease manifest: %w", err)
	}
	return named.WriteCell(ctx, l.drv, l.name, fileBytes, exclusive)
}

// read loads and verifies the lease cell, returning the decoded Record.
// An absent cell surfaces as errs.ErrArtifactNotFound (an empty slot to
// Acquire). A cell that fails to decode/verify, or whose inline payload
// is not a valid Record, is treated as reclaimable — ErrAlreadyExists
// routes Acquire to the exclusive-create path, which a concurrent
// writer guards (same contract as the previous raw-JSON form).
func (l *Lease) read(ctx context.Context) (Record, error) {
	m, err := named.LoadCell(ctx, l.drv, leaseHashes, l.name)
	if err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return Record{}, err
		}
		return Record{}, errs.ErrAlreadyExists
	}
	var r Record
	if err := json.Unmarshal(m.InlineBlob, &r); err != nil {
		return Record{}, errs.ErrAlreadyExists
	}
	return r, nil
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// isNotFound reports whether err signals an absent lease cell. LoadCell
// maps an absent cell to errs.ErrArtifactNotFound; fs.ErrNotExist is
// also accepted defensively for any driver that surfaces it directly.
func isNotFound(err error) bool {
	return errors.Is(err, errs.ErrArtifactNotFound) || errors.Is(err, fs.ErrNotExist)
}

// --- bootstrap hash ---

// leaseHashAlgo is the fixed content-hash algorithm for lease cells.
const leaseHashAlgo = "sha256"

// leaseHashes is a minimal, sha256-only HashRegistry compiled into the
// binary. A lease is acquired before the store's configurable registry
// is wired (location.lock at OpenStore), so it cannot depend on that
// registry; sha256 is always linked in. Because sha256 is universal, a
// cell written here is still readable later through any store's registry
// (verify-on-read in SystemStore.Get/Walk).
var leaseHashes domain.HashRegistry = sha256Registry{}

type sha256Registry struct{}

func (sha256Registry) Parse(h string) (string, []byte, error) {
	i := strings.IndexByte(h, '-')
	if i <= 0 {
		return "", nil, fmt.Errorf("lease: malformed hash id %q", h)
	}
	raw, err := hex.DecodeString(h[i+1:])
	if err != nil {
		return "", nil, err
	}
	return h[:i], raw, nil
}

func (sha256Registry) NewHasher(algo string) (hash.Hash, error) {
	if algo == leaseHashAlgo {
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("lease: unknown hash algo %q", algo)
}

func (sha256Registry) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

func (r sha256Registry) Register(string, func() hash.Hash) domain.HashRegistry { return r }

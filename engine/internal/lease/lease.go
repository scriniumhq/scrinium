package lease

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/timefmt"
	"scrinium.dev/errs"
)

// Record is the in-memory lease body. On disk it is one line of JSON
// (§11.2); the AcquiredAt/ExpiresAt timestamps are encoded through
// timefmt — the canonical RFC3339-second-UTC format the durable layer
// (index/sqlite, manifestcodec) shares — so a lease written by one
// subsystem parses byte-identically in another. Custom JSON methods
// keep callers working with time.Time while the wire form stays
// canonical.
type Record struct {
	HostID     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
	AgentType  string
	Nonce      string
}

// recordWire is the on-disk JSON shape: timestamps as canonical
// timefmt strings rather than Go's default RFC3339Nano-with-offset.
type recordWire struct {
	HostID     string `json:"host_id"`
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt  string `json:"expires_at"`
	AgentType  string `json:"agent_type"`
	Nonce      string `json:"nonce"`
}

// MarshalJSON encodes the record with timefmt-formatted timestamps.
func (r Record) MarshalJSON() ([]byte, error) {
	return json.Marshal(recordWire{
		HostID:     r.HostID,
		AcquiredAt: timefmt.Format(r.AcquiredAt),
		ExpiresAt:  timefmt.Format(r.ExpiresAt),
		AgentType:  r.AgentType,
		Nonce:      r.Nonce,
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
	return nil
}

// expired reports whether the lease is past its TTL relative to now.
func (r Record) expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Lease is a held lease. It is not safe for concurrent use by multiple
// goroutines except that Renew and Release may run on a heartbeat
// goroutine while the holder reads the immutable identity fields.
type Lease struct {
	drv       driver.Driver
	path      string
	hostID    string
	agentType string
	ttl       time.Duration
	nonce     string
}

// Config configures Acquire.
type Config struct {
	// Path is the full Driver path of the lease file, e.g.
	// "system.state/maintenance/lease". Required.
	Path string

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
}

// Acquire takes the lease per §11.2. It succeeds when the slot is
// empty (exclusive create wins) or the current lease has expired
// (takeover). It returns ErrLeaseHeld when a live lease is held by a
// different host.
//
// On a takeover of an expired lease, prev is the record that was
// overwritten (so the caller can emit a stale-lease takeover event);
// prev is nil when the slot was empty.
func Acquire(ctx context.Context, drv driver.Driver, cfg Config) (l *Lease, prev *Record, err error) {
	if cfg.Path == "" || cfg.HostID == "" || cfg.TTL <= 0 {
		return nil, nil, fmt.Errorf("lease.Acquire: Path, HostID and TTL>0 are required")
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, nil, fmt.Errorf("lease.Acquire: nonce: %w", err)
	}
	l = &Lease{
		drv:       drv,
		path:      cfg.Path,
		hostID:    cfg.HostID,
		agentType: cfg.AgentType,
		ttl:       cfg.TTL,
		nonce:     nonce,
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
	if !current.expired(now) && current.HostID != cfg.HostID {
		return nil, nil, errs.ErrLeaseHeld
	}

	// Expired (or ours): overwrite, then read back and verify our
	// nonce won — settles concurrent takeover without a coordinator.
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
	if current.expired(now) && current.HostID != cfg.HostID {
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
	if err := l.drv.Remove(ctx, l.path); err != nil && !isNotFound(err) {
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
	}
}

func (l *Lease) write(ctx context.Context, exclusive bool) error {
	body, err := json.Marshal(l.record())
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	var opts []driver.PutOption
	if exclusive {
		opts = append(opts, driver.WithExclusive())
	}
	return l.drv.Put(ctx, l.path, strings.NewReader(string(body)), opts...)
}

func (l *Lease) read(ctx context.Context) (Record, error) {
	rc, err := l.drv.Get(ctx, l.path)
	if err != nil {
		return Record{}, err
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return Record{}, fmt.Errorf("read body: %w", err)
	}
	var r Record
	if err := json.Unmarshal(body, &r); err != nil {
		// A corrupt/partial lease body is treated as absent: the slot
		// is reclaimable. Returning ErrAlreadyExists routes Acquire to
		// the exclusive-create path, which a concurrent writer guards.
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

// isNotFound reports whether err signals an absent lease file. Drivers
// return an fs.ErrNotExist-wrapping error from Get/Remove on a missing
// path (localfs returns the raw os.Open error; see engine/driver/localfs).
func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

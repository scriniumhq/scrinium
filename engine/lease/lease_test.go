package lease_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/named"
	"scrinium.dev/engine/lease"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
)

const (
	leaseName = "store.agent.maintenance.lease"
	hostA     = "host-A"
	hostB     = "host-B"
)

// leaseTestHashes is a sha256-only HashRegistry mirroring the lease's own
// compiled-in registry, so a cell staged by these fixtures decodes and
// verifies exactly as a lease-written one.
type leaseTestHashes struct{}

func (leaseTestHashes) Parse(h string) (string, []byte, error) {
	i := strings.IndexByte(h, '-')
	if i <= 0 {
		return "", nil, fmt.Errorf("bad hash id %q", h)
	}
	raw, err := hex.DecodeString(h[i+1:])
	return h[:i], raw, err
}

func (leaseTestHashes) NewHasher(algo string) (hash.Hash, error) {
	if algo == "sha256" {
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("unknown algo %q", algo)
}

func (leaseTestHashes) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

func (h leaseTestHashes) Register(string, func() hash.Hash) domain.HashRegistry { return h }

func cfgA(ttl time.Duration) lease.Config {
	return lease.Config{Name: leaseName, HostID: hostA, AgentType: "Test", TTL: ttl}
}

// writeRecord stages a fully-formed lease record as the lease cell,
// bypassing Acquire — used to stage live/expired/foreign states
// deterministically. The record is wrapped in the same inline-manifest
// form (sha256) the lease writes, so the lease's own read decodes it.
func writeRecord(t *testing.T, drv driver.Driver, rec lease.Record) {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	fileBytes, _, err := named.BuildInlineManifest(body, "sha256", leaseTestHashes{})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	if err := named.WriteCell(context.Background(), drv, leaseName, fileBytes, false); err != nil {
		t.Fatalf("write cell: %v", err)
	}
}

// putRaw writes an arbitrary (possibly corrupt) body directly at the
// lease cell path, bypassing the manifest form.
func putRaw(t *testing.T, drv driver.Driver, body string) {
	t.Helper()
	path, err := named.CellPath(leaseName)
	if err != nil {
		t.Fatalf("cell path: %v", err)
	}
	if err := drv.Put(context.Background(), path, strings.NewReader(body)); err != nil {
		t.Fatalf("put raw: %v", err)
	}
}

// readRecord loads and parses the lease cell. ok is false when absent.
func readRecord(t *testing.T, drv driver.Driver) (rec lease.Record, ok bool) {
	t.Helper()
	m, err := named.LoadCell(context.Background(), drv, leaseTestHashes{}, leaseName)
	if err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return lease.Record{}, false
		}
		t.Fatalf("load cell: %v", err)
	}
	if err := json.Unmarshal(m.InlineBlob, &rec); err != nil {
		t.Fatalf("unmarshal inline blob %q: %v", m.InlineBlob, err)
	}
	return rec, true
}

// readRaw returns the lease cell file body verbatim (empty when absent).
func readRaw(t *testing.T, drv driver.Driver) string {
	t.Helper()
	path, err := named.CellPath(leaseName)
	if err != nil {
		t.Fatalf("cell path: %v", err)
	}
	rc, err := drv.Get(context.Background(), path)
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}

func TestAcquire_EmptySlot(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, prev, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if l == nil {
		t.Fatal("Acquire returned nil lease")
	}
	if prev != nil {
		t.Fatalf("prev = %+v, want nil on empty slot", prev)
	}
	rec, ok := readRecord(t, drv)
	if !ok {
		t.Fatal("no lease file written")
	}
	if rec.HostID != hostA {
		t.Errorf("HostID = %q, want %q", rec.HostID, hostA)
	}
	if rec.AgentType != "Test" {
		t.Errorf("AgentType = %q, want %q", rec.AgentType, "Test")
	}
	if rec.Nonce == "" {
		t.Error("Nonce is empty")
	}
	if !rec.ExpiresAt.After(rec.AcquiredAt) {
		t.Errorf("ExpiresAt %v not after AcquiredAt %v", rec.ExpiresAt, rec.AcquiredAt)
	}
}

func TestAcquire_HeldByLiveForeign(t *testing.T) {
	drv := driverfx.LocalFS(t)
	now := time.Now()
	writeRecord(t, drv, lease.Record{
		HostID:     hostB,
		AcquiredAt: now,
		ExpiresAt:  now.Add(time.Hour), // live
		AgentType:  "Other",
		Nonce:      "AAAAAAAAAAAAAAAAAAAAAA==",
	})
	_, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if !errors.Is(err, errs.ErrLeaseHeld) {
		t.Fatalf("Acquire err = %v, want ErrLeaseHeld", err)
	}
	if rec, _ := readRecord(t, drv); rec.HostID != hostB {
		t.Errorf("lease overwritten: HostID = %q, want %q (untouched)", rec.HostID, hostB)
	}
}

func TestAcquire_TakeoverExpiredForeign(t *testing.T) {
	drv := driverfx.LocalFS(t)
	now := time.Now()
	writeRecord(t, drv, lease.Record{
		HostID:     hostB,
		AcquiredAt: now.Add(-2 * time.Hour),
		ExpiresAt:  now.Add(-time.Hour), // expired
		AgentType:  "Other",
		Nonce:      "BBBBBBBBBBBBBBBBBBBBBB==",
	})
	l, prev, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire (takeover): %v", err)
	}
	if l == nil {
		t.Fatal("nil lease after takeover")
	}
	if prev == nil {
		t.Fatal("prev = nil, want the overwritten expired record")
	}
	if prev.HostID != hostB {
		t.Errorf("prev.HostID = %q, want %q", prev.HostID, hostB)
	}
	if rec, _ := readRecord(t, drv); rec.HostID != hostA {
		t.Errorf("after takeover HostID = %q, want %q", rec.HostID, hostA)
	}
}

func TestAcquire_ReacquireOwnLiveRefreshesNonce(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute)); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	rec1, _ := readRecord(t, drv)

	_, prev, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("re-Acquire own: %v", err)
	}
	if prev != nil {
		t.Errorf("prev = %+v, want nil (own lease, not a foreign takeover)", prev)
	}
	rec2, _ := readRecord(t, drv)
	if rec2.HostID != hostA {
		t.Errorf("HostID = %q, want %q", rec2.HostID, hostA)
	}
	if rec2.Nonce == rec1.Nonce {
		t.Error("nonce unchanged on re-Acquire; want a fresh hold nonce")
	}
}

func TestAcquire_Validation(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cases := map[string]lease.Config{
		"empty name": {Name: "", HostID: hostA, TTL: time.Minute},
		"empty host": {Name: leaseName, HostID: "", TTL: time.Minute},
		"zero ttl":   {Name: leaseName, HostID: hostA, TTL: 0},
		"neg ttl":    {Name: leaseName, HostID: hostA, TTL: -time.Second},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := lease.Acquire(context.Background(), drv, c); err == nil {
				t.Fatalf("Acquire(%+v) = nil err, want validation error", c)
			}
		})
	}
}

func TestAcquire_CorruptBodyBlocks(t *testing.T) {
	drv := driverfx.LocalFS(t)
	putRaw(t, drv, "}{ not json")
	// A corrupt/unparseable lease body is conservative: Acquire refuses
	// rather than stomping a file it cannot read.
	if _, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute)); err == nil {
		t.Fatal("Acquire on corrupt body = nil err, want failure")
	}
	if got := readRaw(t, drv); got != "}{ not json" {
		t.Errorf("corrupt body was overwritten: %q", got)
	}
}

func TestRenew_ExtendsExpiry(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rec0, _ := readRecord(t, drv)
	// Lease timestamps are persisted via timefmt at second precision,
	// so the gap before Renew must cross a whole second for ExpiresAt
	// to advance observably.
	time.Sleep(1100 * time.Millisecond)
	if err := l.Renew(context.Background()); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	rec1, _ := readRecord(t, drv)
	if !rec1.ExpiresAt.After(rec0.ExpiresAt) {
		t.Errorf("ExpiresAt not extended: %v -> %v", rec0.ExpiresAt, rec1.ExpiresAt)
	}
	if rec1.HostID != rec0.HostID || rec1.Nonce != rec0.Nonce {
		t.Errorf("identity changed on Renew: host %q/%q nonce %q/%q",
			rec0.HostID, rec1.HostID, rec0.Nonce, rec1.Nonce)
	}
}

func TestRenew_LostAfterForeignTakeover(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Simulate another host taking over.
	now := time.Now()
	writeRecord(t, drv, lease.Record{
		HostID: hostB, AcquiredAt: now, ExpiresAt: now.Add(time.Hour),
		AgentType: "Other", Nonce: "CCCCCCCCCCCCCCCCCCCCCC==",
	})
	if err := l.Renew(context.Background()); !errors.Is(err, errs.ErrLeaseLost) {
		t.Fatalf("Renew after takeover = %v, want ErrLeaseLost", err)
	}
}

func TestRenew_LostWhenDeleted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := named.RemoveCell(context.Background(), drv, leaseName); err != nil {
		t.Fatalf("RemoveCell: %v", err)
	}
	if err := l.Renew(context.Background()); !errors.Is(err, errs.ErrLeaseLost) {
		t.Fatalf("Renew after delete = %v, want ErrLeaseLost", err)
	}
}

func TestRelease_DeletesOwn(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, ok := readRecord(t, drv); ok {
		t.Error("lease file still present after Release")
	}
}

func TestRelease_NotOursIsNoOp(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Another host takes over before we release.
	now := time.Now()
	writeRecord(t, drv, lease.Record{
		HostID: hostB, AcquiredAt: now, ExpiresAt: now.Add(time.Hour),
		AgentType: "Other", Nonce: "DDDDDDDDDDDDDDDDDDDDDD==",
	})
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("Release (not ours) = %v, want nil no-op", err)
	}
	if rec, ok := readRecord(t, drv); !ok || rec.HostID != hostB {
		t.Error("Release deleted a foreign holder's lease")
	}
}

func TestRelease_AbsentIsNoOp(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(time.Minute))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := named.RemoveCell(context.Background(), drv, leaseName); err != nil {
		t.Fatalf("RemoveCell: %v", err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("Release (absent) = %v, want nil", err)
	}
}

func TestHeartbeat_RenewsThenStopsOnCancel(t *testing.T) {
	drv := driverfx.LocalFS(t)
	// TTL 2s → renew interval 1s, which crosses a whole second so the
	// timefmt-persisted ExpiresAt advances observably between ticks.
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(2*time.Second))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rec0, _ := readRecord(t, drv)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Heartbeat(ctx) }()

	// Let at least one renew tick (interval = TTL/2 = 1s) fire.
	time.Sleep(2300 * time.Millisecond)
	rec1, ok := readRecord(t, drv)
	if !ok {
		t.Fatal("lease gone during heartbeat")
	}
	if !rec1.ExpiresAt.After(rec0.ExpiresAt) {
		t.Errorf("heartbeat did not renew: %v -> %v", rec0.ExpiresAt, rec1.ExpiresAt)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Heartbeat returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Heartbeat did not return after cancel")
	}
}

func TestHeartbeat_AbortsOnTakeover(t *testing.T) {
	drv := driverfx.LocalFS(t)
	l, _, err := lease.Acquire(context.Background(), drv, cfgA(2*time.Second))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Heartbeat(context.Background()) }()

	// Foreign takeover: the next Renew tick must observe a lost lease.
	now := time.Now()
	writeRecord(t, drv, lease.Record{
		HostID: hostB, AcquiredAt: now, ExpiresAt: now.Add(time.Hour),
		AgentType: "Other", Nonce: "EEEEEEEEEEEEEEEEEEEEEE==",
	})
	select {
	case err := <-done:
		if !errors.Is(err, errs.ErrLeaseLost) {
			t.Fatalf("Heartbeat returned %v, want ErrLeaseLost", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Heartbeat did not abort after takeover")
	}
}

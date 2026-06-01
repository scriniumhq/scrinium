package lease_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/lease"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
)

const (
	leasePath = "system.state/maintenance/lease"
	hostA     = "host-A"
	hostB     = "host-B"
)

func cfgA(ttl time.Duration) lease.Config {
	return lease.Config{Path: leasePath, HostID: hostA, AgentType: "Test", TTL: ttl}
}

// writeRecord puts a fully-formed lease record at leasePath, bypassing
// Acquire — used to stage live/expired/foreign states deterministically.
func writeRecord(t *testing.T, drv driver.Driver, rec lease.Record) {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if err := drv.Put(context.Background(), leasePath, strings.NewReader(string(body))); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

// putRaw writes an arbitrary (possibly corrupt) body at leasePath.
func putRaw(t *testing.T, drv driver.Driver, body string) {
	t.Helper()
	if err := drv.Put(context.Background(), leasePath, strings.NewReader(body)); err != nil {
		t.Fatalf("put raw: %v", err)
	}
}

// readRecord reads and parses the lease file. ok is false when absent.
func readRecord(t *testing.T, drv driver.Driver) (rec lease.Record, ok bool) {
	t.Helper()
	rc, err := drv.Get(context.Background(), leasePath)
	if err != nil {
		return lease.Record{}, false
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("unmarshal body %q: %v", b, err)
	}
	return rec, true
}

// readRaw returns the lease file body verbatim (empty when absent).
func readRaw(t *testing.T, drv driver.Driver) string {
	t.Helper()
	rc, err := drv.Get(context.Background(), leasePath)
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
		"empty path": {Path: "", HostID: hostA, TTL: time.Minute},
		"empty host": {Path: leasePath, HostID: "", TTL: time.Minute},
		"zero ttl":   {Path: leasePath, HostID: hostA, TTL: 0},
		"neg ttl":    {Path: leasePath, HostID: hostA, TTL: -time.Second},
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
	if err := drv.Remove(context.Background(), leasePath); err != nil {
		t.Fatalf("Remove: %v", err)
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
	if err := drv.Remove(context.Background(), leasePath); err != nil {
		t.Fatalf("Remove: %v", err)
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

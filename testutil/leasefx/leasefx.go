// Package leasefx provides test fixtures for the named-store lease.
//
// It stages a lease through the real namedstore.Acquire — the on-disk
// record (cell manifest, nonce, timestamps) is therefore always
// authoritative, instead of being hand-rolled in every agent's tests.
// It lives in testutil alongside the other fixtures and imports the
// lease from its home package, engine/namedstore.
package leasefx

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/lease"
)

// StageForeign acquires the lease named name under a foreign host so a
// subsequent agent run on the local host observes it as held. ttl should
// outlast the test (e.g. time.Hour) so the staged lease never lapses
// mid-run without a heartbeat. The lease is released on test cleanup.
func StageForeign(t *testing.T, drv driver.Driver, name, host, agentType string, ttl time.Duration) {
	t.Helper()
	l, _, err := lease.Acquire(context.Background(), drv, lease.Config{
		Name:      name,
		HostID:    host,
		AgentType: agentType,
		TTL:       ttl,
	})
	if err != nil {
		t.Fatalf("leasefx.StageForeign(%q): %v", name, err)
	}
	t.Cleanup(func() { _ = l.Release(context.WithoutCancel(context.Background())) })
}

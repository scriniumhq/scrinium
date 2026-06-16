// Package leasefx provides test fixtures for the agent maintenance lease.
//
// It lives under engine/agent/internal so it can import the internal
// lease package and stage a lease through the real lease.Acquire — the
// on-disk record (format, nonce, timestamps) is therefore always
// authoritative, instead of being hand-rolled as JSON in every agent's
// tests.
package leasefx

import (
	"context"
	"testing"
	"time"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/namedstore"
)

// StageForeign acquires the lease at path under a foreign host so a
// subsequent agent run on the local host observes it as held. ttl should
// outlast the test (e.g. time.Hour) so the staged lease never lapses
// mid-run without a heartbeat. The lease is released on test cleanup.
func StageForeign(t *testing.T, drv driver.Driver, path, host, agentType string, ttl time.Duration) {
	t.Helper()
	l, _, err := namedstore.Acquire(context.Background(), drv, namedstore.Config{
		Path:      path,
		HostID:    host,
		AgentType: agentType,
		TTL:       ttl,
	})
	if err != nil {
		t.Fatalf("leasefx.StageForeign(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = l.Release(context.WithoutCancel(context.Background())) })
}

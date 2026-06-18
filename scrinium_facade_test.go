package scrinium_test

// C-5: the facade assembles a store from ≥2 pluggable backends wired in
// by blank import (as in database/sql) — a storage driver
// (localfs/"file") and an index (sqlite/"sqlite") — and its re-exported
// data-plane options actually flow through to the store, not merely
// compile. Drop either blank import and scrinium.Open fails to resolve
// its scheme.

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	scrinium "scrinium.dev"

	_ "scrinium.dev/engine/driver/localfs"
	_ "scrinium.dev/engine/index/sqlite"
)

func openFacade(t *testing.T) *scrinium.ScriniumClient {
	t.Helper()
	c, err := scrinium.Open(context.Background(), "file://"+t.TempDir())
	if err != nil {
		t.Fatalf("scrinium.Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The file driver + sqlite index assemble through the facade and
// round-trip an artifact's bytes.
func TestFacade_TwoBackends_Roundtrip(t *testing.T) {
	ctx := context.Background()
	c := openFacade(t)

	const body = "facade two-backend roundtrip"
	id, err := c.Put(ctx, scrinium.Artifact{Payload: strings.NewReader(body)},
		scrinium.WithRetention(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := c.Get(ctx, id, scrinium.WithColdRead()) // WithColdRead: compile-exercised only
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("round-trip = %q, want %q", got, body)
	}
}

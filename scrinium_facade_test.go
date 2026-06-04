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

// The re-exported WithNamespace has its documented EFFECT, not just a
// passing type: namespace is part of the manifest, so identical content
// stored under two namespaces yields two distinct ArtifactIDs (and both
// read back). A mis-aliased re-export would still compile but fail here.
func TestFacade_NamespaceOptionHasEffect(t *testing.T) {
	ctx := context.Background()
	c := openFacade(t)

	const body = "same content, two namespaces"
	idA, err := c.Put(ctx, scrinium.Artifact{Payload: strings.NewReader(body)},
		scrinium.WithNamespace("alpha"))
	if err != nil {
		t.Fatalf("Put alpha: %v", err)
	}
	idB, err := c.Put(ctx, scrinium.Artifact{Payload: strings.NewReader(body)},
		scrinium.WithNamespace("beta"))
	if err != nil {
		t.Fatalf("Put beta: %v", err)
	}
	if idA == idB {
		t.Fatalf("WithNamespace had no effect: same ArtifactID %q for distinct namespaces", idA)
	}

	// Both namespaced artifacts read back to the original content.
	rhA, err := c.Get(ctx, idA)
	if err != nil {
		t.Fatalf("Get alpha: %v", err)
	}
	gotA, _ := io.ReadAll(rhA)
	rhA.Close()
	if string(gotA) != body {
		t.Errorf("alpha = %q, want %q", gotA, body)
	}

	rhB, err := c.Get(ctx, idB)
	if err != nil {
		t.Fatalf("Get beta: %v", err)
	}
	gotB, _ := io.ReadAll(rhB)
	rhB.Close()
	if string(gotB) != body {
		t.Errorf("beta = %q, want %q", gotB, body)
	}
}

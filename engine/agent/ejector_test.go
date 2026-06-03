package agent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/testutil/artifactfx"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

func newEjector(t *testing.T, rec *eventfx.Recorder, cfg EjectorConfig) (Ejector, store.Store) {
	t.Helper()
	st, _, _ := storefx.InitShared(t, store.WithPublisher(rec))
	if cfg.TempDir == "" {
		cfg.TempDir = t.TempDir()
	}
	a, err := NewEjector(st, rec, "store-eject", cfg)
	if err != nil {
		t.Fatalf("NewEjector: %v", err)
	}
	return a, st
}

func TestEjector_EjectMaterialises(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{})

	const body = "eject me, please"
	id, err := st.Put(ctx, artifactfx.Payload(body), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	path, err := a.Eject(ctx, id)
	if err != nil {
		t.Fatalf("Eject: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ejected file: %v", err)
	}
	if string(got) != body {
		t.Errorf("ejected bytes = %q, want %q", got, body)
	}
	if rec.Count(event.EventArtifactEjected) != 1 {
		t.Errorf("EventArtifactEjected = %d, want 1", rec.Count(event.EventArtifactEjected))
	}
}

func TestEjector_EjectIdempotent(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{})

	id, err := st.Put(ctx, artifactfx.Payload("twice"), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	p1, err := a.Eject(ctx, id)
	if err != nil {
		t.Fatalf("Eject #1: %v", err)
	}
	p2, err := a.Eject(ctx, id)
	if err != nil {
		t.Fatalf("Eject #2: %v", err)
	}
	if p1 != p2 {
		t.Errorf("idempotent Eject differs: %q vs %q", p1, p2)
	}
	if n := rec.Count(event.EventArtifactEjected); n != 1 {
		t.Errorf("EventArtifactEjected = %d, want 1 (reuse)", n)
	}
}

// Two artifacts with identical content but different namespaces have
// different ArtifactIDs and the same ContentHash; eject must dedup them
// to one file (CAS naming).
func TestEjector_ContentAddressedDedup(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{})

	const body = "shared content across two artifacts"
	idA, err := st.Put(ctx, artifactfx.Payload(body), domain.WithNamespace("a"))
	if err != nil {
		t.Fatalf("Put A: %v", err)
	}
	idB, err := st.Put(ctx, artifactfx.Payload(body), domain.WithNamespace("b"))
	if err != nil {
		t.Fatalf("Put B: %v", err)
	}
	if idA == idB {
		t.Fatalf("expected distinct ArtifactIDs for different namespaces")
	}

	pA, err := a.Eject(ctx, idA)
	if err != nil {
		t.Fatalf("Eject A: %v", err)
	}
	pB, err := a.Eject(ctx, idB)
	if err != nil {
		t.Fatalf("Eject B: %v", err)
	}
	if pA != pB {
		t.Errorf("CAS dedup failed: %q vs %q", pA, pB)
	}
	if n := rec.Count(event.EventArtifactEjected); n != 1 {
		t.Errorf("EventArtifactEjected = %d, want 1 (second is dedup reuse)", n)
	}
}

func TestEjector_HoldProtectsFromIdleEviction(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{KeepAliveIdle: time.Millisecond})

	id, err := st.Put(ctx, artifactfx.Payload("held"), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	h, err := a.Hold(ctx, id)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := a.Run(ctx); err != nil {
		t.Fatalf("Run (held): %v", err)
	}
	if _, err := os.Stat(h.Path()); err != nil {
		t.Errorf("held file evicted while holder alive: %v", err)
	}

	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	res, err := a.Run(ctx)
	if err != nil {
		t.Fatalf("Run (released): %v", err)
	}
	if res.Stats["evicted"] != 1 {
		t.Errorf("evicted = %d, want 1 after release", res.Stats["evicted"])
	}
	if _, err := os.Stat(h.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still present after idle eviction: %v", err)
	}
	if rec.Count(event.EventEjectorCleanup) < 1 {
		t.Errorf("EventEjectorCleanup not emitted")
	}
}

func TestEjector_EjectFragmentPrefix(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{})

	const body = "HEADER////tail-bytes-not-wanted"
	id, err := st.Put(ctx, artifactfx.Payload(body), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	path, err := a.EjectFragment(ctx, id, 0, 6)
	if err != nil {
		t.Fatalf("EjectFragment: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	if string(got) != "HEADER" {
		t.Errorf("fragment = %q, want %q", got, "HEADER")
	}
}

func TestEjector_EjectFragmentBadRange(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{})
	id, err := st.Put(ctx, artifactfx.Payload("0123456789"), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := a.EjectFragment(ctx, id, 5, 5); !errors.Is(err, errs.ErrInvalidRange) {
		t.Errorf("empty range err = %v, want ErrInvalidRange", err)
	}
	if _, err := a.EjectFragment(ctx, id, 0, 9999); !errors.Is(err, errs.ErrInvalidRange) {
		t.Errorf("out-of-bounds range err = %v, want ErrInvalidRange", err)
	}
}

func TestEjector_EjectFragmentTooLarge(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{MaxFragmentBytes: 4})
	id, err := st.Put(ctx, artifactfx.Payload("0123456789"), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := a.EjectFragment(ctx, id, 0, 8); !errors.Is(err, errs.ErrFragmentTooLarge) {
		t.Errorf("oversized fragment err = %v, want ErrFragmentTooLarge", err)
	}
}

func TestEjector_ClosedRejects(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, st := newEjector(t, rec, EjectorConfig{})
	id, err := st.Put(ctx, artifactfx.Payload("x"), domain.WithNamespace("ej"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := a.Eject(ctx, id); err != nil {
		t.Fatalf("Eject: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := a.Eject(ctx, id); !errors.Is(err, errs.ErrEjectorClosed) {
		t.Errorf("Eject after close = %v, want ErrEjectorClosed", err)
	}
	if err := a.Validate(ctx); !errors.Is(err, errs.ErrEjectorClosed) {
		t.Errorf("Validate after close = %v, want ErrEjectorClosed", err)
	}
}

func TestEjector_EjectNotFound(t *testing.T) {
	ctx := context.Background()
	rec := eventfx.New()
	a, _ := newEjector(t, rec, EjectorConfig{})
	if _, err := a.Eject(ctx, domain.ArtifactID("does-not-exist")); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Eject of missing = %v, want ErrArtifactNotFound", err)
	}
	if rec.Count(event.EventEjectFailed) < 1 {
		t.Errorf("EventEjectFailed not emitted")
	}
}

func TestEjector_Registered(t *testing.T) {
	if _, ok := Lookup("ejector"); !ok {
		t.Error(`Lookup("ejector") = false, want registered`)
	}
}

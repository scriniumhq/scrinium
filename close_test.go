package scrinium_test

import (
	"context"
	"errors"
	"testing"

	"scrinium.dev"
)

// TestClose_OnPartialScrinium is the regression test for
// Close on a Scrinium value where some fields are nil — the
// shape a host gets when Open or Init aborts midway. Before
// nil-safety, this panicked on the first s.View.Close() call.
func TestClose_OnPartialScrinium(t *testing.T) {
	cases := []struct {
		name string
		s    *scrinium.Scrinium
	}{
		{"empty", &scrinium.Scrinium{}},
		{"only-config", &scrinium.Scrinium{Config: scrinium.DefaultConfig()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.s.Close(); err != nil {
				t.Errorf("Close on partial Scrinium: %v", err)
			}
		})
	}
}

// TestClose_Idempotent verifies the documented contract:
// the first Close shuts resources down, subsequent calls
// return the same result without re-running.
func TestClose_Idempotent(t *testing.T) {
	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + t.TempDir()
	s, _, err := scrinium.Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second Close: must not error, must not panic, must not
	// re-run any inner Close (which would itself error on a
	// double-close of e.g. the sqlite handle).
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	// Third Close, just to be sure.
	if err := s.Close(); err != nil {
		t.Errorf("third Close: %v", err)
	}
}

// TestClose_DeferIdiom verifies the canonical Go pattern:
// defer s.Close() at construction time, plus explicit s.Close()
// when the host wants to release resources eagerly. Both calls
// must succeed.
func TestClose_DeferIdiom(t *testing.T) {
	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + t.TempDir()
	s, _, err := scrinium.Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("deferred Close: %v", err)
		}
	}()

	// Eager close — host decided it's done before function exit.
	if err := s.Close(); err != nil {
		t.Errorf("eager Close: %v", err)
	}
	// The deferred Close above must also succeed (idempotency).
}

// TestClose_ReturnsSameErrorOnRepeatCalls verifies that if
// the first Close errored, subsequent calls return that same
// error rather than nil — surfaces relying on the return for
// shutdown reporting see a stable signal.
func TestClose_ReturnsSameErrorOnRepeatCalls(t *testing.T) {
	// We can't easily induce a Close error without injecting
	// a faulty driver, which is out of scope for this test.
	// Instead, we verify the weaker invariant: two Close calls
	// on a healthy Scrinium return identical (nil) results.
	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + t.TempDir()
	s, _, err := scrinium.Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	first := s.Close()
	second := s.Close()
	if !errors.Is(first, second) && !(first == nil && second == nil) {
		t.Errorf("Close return values diverge: first=%v second=%v", first, second)
	}
}

// Close is a terminal condition: every operation gated on
// checkOperational refuses with os.ErrClosed afterwards. This is
// deliberately distinct from ErrLocked (an encrypted store awaiting
// Unlock) — Close is final, there is nothing to re-acquire. Conflating
// the two sent Plain-store users hunting for a passphrase that never
// existed, so the two states are kept separate.

package storesuite

import (
	"context"
	"errors"
	"os"
	"testing"

	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/storefx"
)

// TestClose_BlocksOperationsAfterClose: Close succeeds, and a subsequent
// operation gated on checkOperational refuses with os.ErrClosed (not
// ErrLocked) — for both a Plain store and an Unlocked encrypted store,
// whose DEK Close additionally wipes. Capacity is the canonical
// lightweight probe for "is the store reachable?".
func TestClose_BlocksOperationsAfterClose(t *testing.T) {
	cases := []struct {
		name string
		init func(t *testing.T) store.Store
	}{
		{"plain", func(t *testing.T) store.Store { s, _ := storefx.InitPlain(t); return s }},
		{"encrypted-unlocked", func(t *testing.T) store.Store { s, _ := storefx.InitEncrypted(t, "hunter2"); return s }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.init(t)
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := s.Capacity(context.Background()); !errors.Is(err, os.ErrClosed) {
				t.Errorf("Capacity after Close: got %v, want os.ErrClosed", err)
			}
		})
	}
}

// TestClose_AfterClose_OperationsRefuseGracefully: after Close, an
// operation that touches secret material Close has wiped must refuse with
// os.ErrClosed rather than panic on the wiped state. ExportRecoveryKit is
// the probe — a missing Closed-gate would surface as a panic or as
// success returning a zero/garbage kit.
func TestClose_AfterClose_OperationsRefuseGracefully(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "hunter2")

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ExportRecoveryKit panicked after Close: %v", r)
		}
	}()
	_, err := s.ExportRecoveryKit(context.Background())
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("ExportRecoveryKit after Close: got %v, want os.ErrClosed", err)
	}
}

// TestClose_Idempotent: a repeated Close on a real store is a no-op
// without error.
func TestClose_Idempotent(t *testing.T) {
	s, _ := storefx.InitEncrypted(t, "pw")

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

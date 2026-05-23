package store_test

import (
	"testing"

	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/storefx"
	"scrinium.dev/internal/testutil/systemstoretest"
)

// TestSystemStoreConformance runs the shared SystemStore conformance
// suite against the engine's default in-process implementation.
//
// The factory builds a real Plain store via storefx.InitPlain and
// hands back its System() facade plus the same StoreIndex instance
// (through the Reopener) so the suite can assert on index routing.
// Each subtest gets a fresh store; the driver and index register
// their own t.Cleanup inside the fixtures, so cleanup only needs to
// close the store.
func TestSystemStoreConformance(t *testing.T) {
	systemstoretest.Run(t, systemstoretest.Factory{
		New: func(t *testing.T) (store.SystemStore, index.StoreIndex, func()) {
			s, r := storefx.InitPlain(t)
			return s.System(), r.Index(), func() { _ = s.Close() }
		},
	})
}

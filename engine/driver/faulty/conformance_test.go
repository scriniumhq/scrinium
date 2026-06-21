package faulty

import (
	"testing"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/driver/drivertest"
)

// TestConformance runs the shared Driver conformance suite against a
// faulty.Driver with an empty fault profile. With no failure rate,
// latency, or fail-on-call configured, every method is a straight
// pass-through to the inner driver, so the wrapper must satisfy the
// full Driver contract exactly as its backend (localfs) does.
//
// This is the executable form of the guarantee stated in drivertest's
// package doc: the faulty driver injecting zero faults is expected to
// pass the suite unchanged. It guards against the wrapper silently
// altering happy-path semantics — swallowing an error, mangling a
// return value, or mishandling a concurrent path.
//
// Fault-injection behaviour itself (failure rates, latency, the
// deterministic fail-on-call trip, call counting) is covered by
// faulty_test.go.
func TestConformance(t *testing.T) {
	drivertest.Run(t, drivertest.Factory{
		Name: "faulty-passthrough",
		New: func(t *testing.T) driver.Driver {
			// newWrapped (faulty_test.go) builds a localfs inner in a
			// t.TempDir() and wraps it; with no options the fault
			// profile is empty — a transparent pass-through.
			return newWrapped(t)
		},
	})
}

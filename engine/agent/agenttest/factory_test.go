package agenttest

import (
	"testing"

	"scrinium.dev/engine/agent"
	_ "scrinium.dev/engine/agent/preset"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// TestFactory_ConfigTypeMismatchDefaults checks that a built-in factory
// tolerates a Config of the wrong type. The Scheduler/host hands
// Schedule.Config straight to Build, and factories decode it with a
// comma-ok assertion (zero value -> defaults), so Build must succeed
// rather than error or panic.
func TestFactory_ConfigTypeMismatchDefaults(t *testing.T) {
	rec := eventfx.New()
	st, drv, idx := storefx.InitShared(t, store.WithPublisher(rec))
	deps := agent.AgentDeps{
		Publisher: rec,
		Driver:    drv,
		Index:     idx,
		HostID:    "agenttest-host",
		StoreID:   "store-x",
	}

	// "gc" is registered via the preset blank import; hand it a config of
	// the wrong type on purpose.
	a, err := agent.Build("gc", st, "not-a-GCConfig", deps)
	if err != nil {
		t.Fatalf("Build(gc) with mismatched config = %v, want nil (factory defaults)", err)
	}
	if a == nil {
		t.Fatal("Build(gc) returned a nil agent")
	}
}

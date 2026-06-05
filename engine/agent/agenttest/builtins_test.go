package agenttest

import (
	"testing"

	"scrinium.dev/engine/agent"
	_ "scrinium.dev/engine/agent/preset"
)

// TestBuiltinsRegistered checks that blank-importing the preset bundle
// registers every built-in agent factory (each subpackage's register.go
// init runs on import).
func TestBuiltinsRegistered(t *testing.T) {
	for _, kind := range []string{"gc", "scrub", "checkpoint", "rebuild", "ejector"} {
		if _, ok := agent.Lookup(kind); !ok {
			t.Errorf("Lookup(%q) = false, want a registered built-in factory", kind)
		}
	}
}

// Package preset blank-imports the built-in Scrinium agents so a single
// import wires the standard set into the agent registry (ADR-68). The
// assembler imports this to make gc/scrub/checkpoint/rebuild/ejector
// available to agent.Build without naming each one.
package preset

import (
	_ "scrinium.dev/engine/agent/checkpoint"
	_ "scrinium.dev/engine/agent/ejector"
	_ "scrinium.dev/engine/agent/gc"
	_ "scrinium.dev/engine/agent/rebuild"
	_ "scrinium.dev/engine/agent/scrub"
)

package rebuild

import "scrinium.dev/engine/agent"

// init registers the rebuild agent factory with the agent registry, so a
// blank import of this package wires it in (ADR-68 SPI; mirrors the
// driver/index register.go convention).
func init() { agent.Register(rebuildFactory{}) }

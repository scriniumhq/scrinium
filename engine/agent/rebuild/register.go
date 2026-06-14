package rebuild

import (
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/store"
)

// rebuildFactory builds the Rebuild agent from the registry (ADR-51).
type rebuildFactory struct{}

func (rebuildFactory) Name() string { return "rebuild" }

func (rebuildFactory) Build(st store.Store, cfg any, deps agent.AgentDeps) (agent.Agent, error) {
	c, _ := cfg.(RebuildConfig) // zero value on mismatch -> defaults
	return NewRebuildIndexAgent(st, deps.Driver, deps.Index, deps.Publisher, deps.HostID, deps.StoreID, c, agent.WithAgentLogger(deps.Logger))
}

// init registers the rebuild agent factory with the agent registry, so a
// blank import of this package wires it in (ADR-68 SPI; mirrors the
// driver/index register.go convention).
func init() { agent.Register(rebuildFactory{}) }

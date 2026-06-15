package ejector

import (
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/store"
)

// ejectorFactory builds the Ejector from the registry (ADR-51).
type ejectorFactory struct{}

func (ejectorFactory) Name() string { return "ejector" }

func (ejectorFactory) Build(st store.Store, cfg any, deps agent.AgentDeps) (agent.Agent, error) {
	c, _ := cfg.(EjectorConfig) // zero value on mismatch -> defaults
	return NewEjector(st, deps.Publisher, deps.StoreID, c, agent.WithAgentLogger(deps.Logger))
}

// init registers the ejector agent factory with the agent registry, so a
// blank import of this package wires it in (ADR-68 SPI; mirrors the
// driver/index register.go convention).
func init() { agent.Register(ejectorFactory{}) }

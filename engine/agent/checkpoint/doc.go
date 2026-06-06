// Package checkpoint implements the Scrinium checkpoint agent:
// periodic index checkpoint capture.
//
// It is a plugin behind the agent registry (ADR-68): a blank import of
// this package registers its factory via register.go, after which the
// assembler builds it through agent.Build. The agent embeds
// agent.BaseState and satisfies the agent.Agent contract.
package checkpoint
